package gates

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

)

// ─── Intent-Gated Execution (IGX) ───────────────────────────────────────────

// Intent is a rank on the configurable intent ladder. The ladder itself
// lives in the intent registry (loaded from config/DB); this type is just
// the integer rank that flows through the gate. Go code never translates
// ranks back to names — naming is the registry's job.
type Intent int

// IntentAuto asks the executive to infer an intent from tool impacts.
// Any non-negative value is a concrete rank from the registry.
const IntentAuto Intent = -1

// String renders a rank for log lines. Go has no knowledge of which name
// a rank corresponds to — that lookup belongs to the caller with access
// to the registry.
func (i Intent) String() string {
	if i == IntentAuto {
		return "auto"
	}
	return fmt.Sprintf("rank(%d)", int(i))
}

// ─── Clearance ──────────────────────────────────────────────────────────────

// ClearanceSource provides the node's current clearance level.
// Clearance is externally managed and locally cached.
type ClearanceSource interface {
	Clearance() int
}

// ─── Audit ──────────────────────────────────────────────────────────────────

// AuditEntry is a single line in the audit log.
type AuditEntry struct {
	Time      string `json:"t"`
	Tool      string `json:"tool"`
	Params    any    `json:"params,omitempty"`
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
	AlertID   string `json:"alert_id,omitempty"`
	TriggerID string `json:"trigger_id,omitempty"`
	Intent    int    `json:"intent,omitempty"`
	Impact    int    `json:"impact,omitempty"`
	Clearance int    `json:"clearance,omitempty"`
}

// ─── Gate ───────────────────────────────────────────────────────────────────

// Gate enforces safety policies on tool execution.
// The Triad Gate checks: tool.Impact(params) <= min(intent, clearance).
type Gate struct {
	mu           sync.Mutex
	maxTurns     int
	rateLimit    int              // max invocations per hour
	invocations  []time.Time      // sliding window
	clearance    ClearanceSource  // nil = deny all (clearance 0)
	lockdown     bool             // when true, all impact>0 is blocked
	auditFile    *os.File         // append-only NDJSON
	auditEncoder *json.Encoder
}

// GateConfig holds configuration for the safety gate.
type GateConfig struct {
	MaxTurns  int
	RateLimit int              // max invocations per hour
	AuditDir  string           // directory for audit.jsonl
	Clearance ClearanceSource  // nil = deny all (clearance 0)
}

// NewGate creates a Gate with the given configuration.
func NewGate(cfg GateConfig) (*Gate, error) {
	g := &Gate{
		maxTurns:  cfg.MaxTurns,
		rateLimit: cfg.RateLimit,
		clearance: cfg.Clearance,
	}

	if cfg.AuditDir != "" {
		if err := os.MkdirAll(cfg.AuditDir, 0700); err != nil {
			return nil, fmt.Errorf("create audit dir: %w", err)
		}
		f, err := os.OpenFile(filepath.Join(cfg.AuditDir, "audit.jsonl"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, fmt.Errorf("open audit log: %w", err)
		}
		g.auditFile = f
		g.auditEncoder = json.NewEncoder(f)
	}

	return g, nil
}

// ─── Triad Gate ─────────────────────────────────────────────────────────────

// CheckTriad enforces the IGX triad: impact <= min(intent, clearance).
// The caller pre-resolves the tool's effective impact via the intent
// registry (so DB overrides apply). Returns nil if allowed, descriptive
// error if blocked.
func (g *Gate) CheckTriad(intent Intent, skillName string, impact int) error {
	// Observe tools (impact 0) always pass
	if impact == 0 {
		return nil
	}

	// Lockdown blocks all impact>0 tools
	g.mu.Lock()
	locked := g.lockdown
	g.mu.Unlock()
	if locked {
		return fmt.Errorf("gate: lockdown active, %s blocked (impact=%d)", skillName, impact)
	}

	// Compute ceiling = min(intent, clearance)
	clr := 0 // no clearance source = deny all non-zero impact
	if g.clearance != nil {
		clr = g.clearance.Clearance()
	}
	ceiling := int(intent)
	if clr < ceiling {
		ceiling = clr
	}

	if impact > ceiling {
		return fmt.Errorf("gate: %s blocked (impact=%d > min(intent=%s, clearance=%d) = %d)",
			skillName, impact, intent, clr, ceiling)
	}

	return nil
}

// CheckTriadWithScope extends CheckTriad with a per-tool scope impact cap.
// impact is the pre-resolved tool impact (from the intent registry).
// scopeMaxImpact is the maximum impact allowed by the user's scope for
// this tool. Pass -1 to disable scope cap (equivalent to CheckTriad).
func (g *Gate) CheckTriadWithScope(intent Intent, skillName string, impact int, scopeMaxImpact int) error {
	if impact == 0 {
		return nil
	}

	g.mu.Lock()
	locked := g.lockdown
	g.mu.Unlock()
	if locked {
		return fmt.Errorf("gate: lockdown active, %s blocked (impact=%d)", skillName, impact)
	}

	// Compute ceiling = min(intent, clearance, scopeCap)
	clr := 0 // no clearance source = deny all non-zero impact
	if g.clearance != nil {
		clr = g.clearance.Clearance()
	}
	ceiling := int(intent)
	if clr < ceiling {
		ceiling = clr
	}
	if scopeMaxImpact >= 0 && scopeMaxImpact < ceiling {
		ceiling = scopeMaxImpact
	}

	if impact > ceiling {
		return fmt.Errorf("gate: %s blocked (impact=%d > min(intent=%s, clearance=%d, scope=%d) = %d)",
			skillName, impact, intent, clr, scopeMaxImpact, ceiling)
	}

	return nil
}

// SetLockdown sets the lockdown flag. When locked down, all impact>0 tools are blocked.
func (g *Gate) SetLockdown(v bool) {
	g.mu.Lock()
	g.lockdown = v
	g.mu.Unlock()
}

// Lockdown returns the current lockdown state.
func (g *Gate) Lockdown() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lockdown
}

// ─── Rate Limit + Turns ─────────────────────────────────────────────────────

// CheckRateLimit returns an error if the hourly rate limit has been exceeded.
func (g *Gate) CheckRateLimit() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-1 * time.Hour)

	// Prune old entries
	valid := g.invocations[:0]
	for _, t := range g.invocations {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	g.invocations = valid

	if len(g.invocations) >= g.rateLimit {
		return fmt.Errorf("rate limit exceeded (%d/%d per hour)", len(g.invocations), g.rateLimit)
	}

	g.invocations = append(g.invocations, now)
	return nil
}

// CheckTurns returns an error if the turn count exceeds the maximum.
func (g *Gate) CheckTurns(n int) error {
	if n >= g.maxTurns {
		return fmt.Errorf("max turns exceeded (%d/%d)", n, g.maxTurns)
	}
	return nil
}

// ─── Audit + Config ─────────────────────────────────────────────────────────

// Audit writes an entry to the audit log.
func (g *Gate) Audit(entry AuditEntry) {
	if g.auditEncoder == nil {
		return
	}
	if entry.Time == "" {
		entry.Time = time.Now().UTC().Format(time.RFC3339)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.auditEncoder.Encode(entry); err != nil {
		log.Printf("[agent] audit write error: %v", err)
	}
}

// Update modifies gate configuration at runtime (from dashboard).
func (g *Gate) Update(rateLimit, maxTurns *int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if rateLimit != nil {
		g.rateLimit = *rateLimit
	}
	if maxTurns != nil {
		g.maxTurns = *maxTurns
	}
}

// Info returns current gate configuration.
func (g *Gate) Info() (rateLimit, maxTurns int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.rateLimit, g.maxTurns
}

// Close releases resources held by the gate.
func (g *Gate) Close() error {
	if g.auditFile != nil {
		return g.auditFile.Close()
	}
	return nil
}
