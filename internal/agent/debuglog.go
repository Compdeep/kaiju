package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ── LLM Trace Log ───────────────────────────────────────────────────────────
//
// Every LLM call in the agent is appended to a per-investigation log file at:
//
//     /tmp/kaiju-prompts/<alertID>.log
//
// Each call is one delimited section showing the input that triggered it,
// the system + user prompts, and the output (or error). The format is
// human-readable and append-only — read it top-to-bottom to follow the
// investigation as it unfolded.
//
// Default: ON — we want these traces when things break. Flip to OFF with
// KAIJU_PROMPT_DEBUG=0 (e.g. for CI or privacy-sensitive runs).
//
// To read:   tail -f /tmp/kaiju-prompts/<alertID>.log

const debugLogDir = "/tmp/kaiju-prompts"

// debugLogEnabled returns true by default. Set KAIJU_PROMPT_DEBUG=0 to
// disable. Cheap; called once per LLM call.
func debugLogEnabled() bool {
	return os.Getenv("KAIJU_PROMPT_DEBUG") != "0"
}

// LLMTrace captures everything we want to record about a single LLM call.
// All fields are optional; only set what's relevant for the caller's
// node type.
type LLMTrace struct {
	AlertID  string    // routes the entry to /tmp/kaiju-prompts/<alertID>.log
	NodeID   string    // n0, n5, etc — empty for pre-graph calls (preflight)
	NodeType string    // "executive", "compute_architect", "debugger", etc
	Tag      string    // node tag from the graph (descriptive label)
	Model    string    // "gpt-4.1", "gpt-4.1-mini" — informational
	Started  time.Time // call start time (defaults to now if zero)

	// Input describing what triggered this LLM call. Free-form key/value;
	// callers add whatever is relevant (query, problem, goal, etc).
	Input map[string]string

	// Optional ContextGate request/response if the call used the gate.
	GateSources  []string // list of source names from the request
	GateBudget   int
	GateSummary  string
	GateReturned map[string]string // verbatim sources

	// The actual prompts sent to the LLM.
	System string
	User   string

	// Result of the call.
	Output    string // LLM response (raw text)
	Err       string // error message if the call failed
	TokensIn  int    // 0 if unknown
	TokensOut int    // 0 if unknown
	LatencyMS int64
}

// debugLogMu serializes appends so concurrent goroutines (e.g. parallel
// coder nodes) don't interleave their entries. Cheap; only contended when
// debug is on.
var debugLogMu sync.Mutex

// WriteLLMTrace appends a single LLM call entry to the per-investigation
// log file. No-op when KAIJU_PROMPT_DEBUG is unset. Errors are silently
// ignored — debug logging must never break the agent.
func WriteLLMTrace(t LLMTrace) {
	if !debugLogEnabled() {
		return
	}
	if t.AlertID == "" {
		t.AlertID = "no_alert"
	}
	if t.Started.IsZero() {
		t.Started = time.Now()
	}

	debugLogMu.Lock()
	defer debugLogMu.Unlock()

	_ = os.MkdirAll(debugLogDir, 0755)
	path := filepath.Join(debugLogDir, t.AlertID+".log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	var sb strings.Builder
	sb.WriteString(t.formatHeader())
	sb.WriteString(t.formatInput())
	sb.WriteString(t.formatGate())
	sb.WriteString(t.formatPrompts())
	sb.WriteString(t.formatOutput())
	sb.WriteString(t.formatMeta())
	sb.WriteString("\n")
	f.WriteString(sb.String())
}

func (t LLMTrace) formatHeader() string {
	id := t.NodeID
	if id == "" {
		id = "(pre-graph)"
	}
	tag := ""
	if t.Tag != "" {
		tag = " " + t.Tag
	}
	model := ""
	if t.Model != "" {
		model = " " + t.Model
	}
	return fmt.Sprintf("\n=== %s %s%s%s %s ===\n",
		id, t.NodeType, tag, model, t.Started.UTC().Format("2006-01-02T15:04:05Z"))
}

func (t LLMTrace) formatInput() string {
	if len(t.Input) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("--- INPUT ---\n")
	// Stable key order
	keys := make([]string, 0, len(t.Input))
	for k := range t.Input {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		sb.WriteString(fmt.Sprintf("%s: %s\n", k, t.Input[k]))
	}
	return sb.String()
}

func (t LLMTrace) formatGate() string {
	if len(t.GateSources) == 0 && t.GateSummary == "" && len(t.GateReturned) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("--- GATE ---\n")
	if len(t.GateSources) > 0 {
		sb.WriteString(fmt.Sprintf("sources: %s\n", strings.Join(t.GateSources, ", ")))
	}
	if t.GateBudget > 0 {
		sb.WriteString(fmt.Sprintf("budget: %d\n", t.GateBudget))
	}
	if t.GateSummary != "" {
		sb.WriteString("\n[curator summary]\n")
		sb.WriteString(t.GateSummary)
		sb.WriteString("\n")
	}
	if len(t.GateReturned) > 0 {
		// Stable key order
		keys := make([]string, 0, len(t.GateReturned))
		for k := range t.GateReturned {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			content := t.GateReturned[k]
			sb.WriteString(fmt.Sprintf("\n[%s — %d chars]\n", k, len(content)))
			sb.WriteString(content)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func (t LLMTrace) formatPrompts() string {
	var sb strings.Builder
	if t.System != "" {
		sb.WriteString("--- SYSTEM PROMPT ---\n")
		sb.WriteString(t.System)
		if !strings.HasSuffix(t.System, "\n") {
			sb.WriteString("\n")
		}
	}
	if t.User != "" {
		sb.WriteString("--- USER PROMPT ---\n")
		sb.WriteString(t.User)
		if !strings.HasSuffix(t.User, "\n") {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func (t LLMTrace) formatOutput() string {
	var sb strings.Builder
	if t.Err != "" {
		sb.WriteString("--- ERROR ---\n")
		sb.WriteString(t.Err)
		if !strings.HasSuffix(t.Err, "\n") {
			sb.WriteString("\n")
		}
	}
	if t.Output != "" {
		sb.WriteString("--- OUTPUT ---\n")
		sb.WriteString(t.Output)
		if !strings.HasSuffix(t.Output, "\n") {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func (t LLMTrace) formatMeta() string {
	if t.LatencyMS == 0 && t.TokensIn == 0 && t.TokensOut == 0 {
		return ""
	}
	return fmt.Sprintf("--- META ---\nlatency_ms: %d\ntokens_in: %d\ntokens_out: %d\n",
		t.LatencyMS, t.TokensIn, t.TokensOut)
}

// sortStrings is a tiny stable string sort. Avoid pulling sort package
// for one use.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
