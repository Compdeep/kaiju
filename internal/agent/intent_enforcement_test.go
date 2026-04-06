package agent

import (
	"path/filepath"
	"testing"

	"github.com/user/kaiju/internal/agent/gates"
	"github.com/user/kaiju/internal/db"
)

// End-to-end enforcement: registry resolves impact → gate enforces. No full
// Agent setup required; we compose the pieces the dispatcher would compose.

// stubClearance returns a fixed clearance level for testing.
type stubClearance struct{ level int }

func (s stubClearance) Clearance() int { return s.level }

func newTestGate(t *testing.T, clearance int) *gates.Gate {
	t.Helper()
	gate, err := gates.NewGate(gates.GateConfig{
		MaxTurns:  10,
		RateLimit: 1000,
		Clearance: stubClearance{level: clearance},
	})
	if err != nil {
		t.Fatalf("new gate: %v", err)
	}
	return gate
}

func newTestStack(t *testing.T) (*IntentRegistry, *gates.Gate, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	if err := database.SeedIntentsFromConfig(testLadder); err != nil {
		t.Fatalf("seed: %v", err)
	}

	reg := NewIntentRegistry()
	if err := reg.Load(database); err != nil {
		t.Fatalf("load registry: %v", err)
	}

	// Clearance high enough to allow all seeded intents
	gate, err := gates.NewGate(gates.GateConfig{
		MaxTurns:  10,
		RateLimit: 1000,
		Clearance: stubClearance{level: 200},
	})
	if err != nil {
		t.Fatalf("new gate: %v", err)
	}
	return reg, gate, database
}

// simulateDispatch mirrors what dispatcher.executeToolNode does for the
// gate check: resolve tool impact via registry, then invoke the gate.
func simulateDispatch(reg *IntentRegistry, gate *gates.Gate, tool *mockTool, requestIntent gates.Intent, scopeCap int) error {
	impact := reg.ResolveToolIntent(tool.Name(), tool, nil)
	return gate.CheckTriadWithScope(requestIntent, tool.Name(), impact, scopeCap)
}

// ── Basic enforcement at each seeded tier ──

func TestEnforce_LowToolAtLowIntent(t *testing.T) {
	reg, gate, _ := newTestStack(t)
	tool := &mockTool{name: "file_read", impact: 0}

	if err := simulateDispatch(reg, gate, tool, gates.Intent(0), -1); err != nil {
		t.Errorf("low-impact tool at low intent should pass: %v", err)
	}
}

func TestEnforce_MidToolAtLowIntent(t *testing.T) {
	reg, gate, _ := newTestStack(t)
	tool := &mockTool{name: "file_write", impact: 100}

	err := simulateDispatch(reg, gate, tool, gates.Intent(0), -1)
	if err == nil {
		t.Error("mid-impact tool at low intent should be blocked")
	}
}

func TestEnforce_MidToolAtMidIntent(t *testing.T) {
	reg, gate, _ := newTestStack(t)
	tool := &mockTool{name: "file_write", impact: 100}

	if err := simulateDispatch(reg, gate, tool, gates.Intent(100), -1); err != nil {
		t.Errorf("mid-impact tool at mid intent should pass: %v", err)
	}
}

func TestEnforce_HighToolAtMidIntent(t *testing.T) {
	reg, gate, _ := newTestStack(t)
	tool := &mockTool{name: "bash", impact: 200}

	err := simulateDispatch(reg, gate, tool, gates.Intent(100), -1)
	if err == nil {
		t.Error("high-impact tool at mid intent should be blocked")
	}
}

func TestEnforce_HighToolAtHighIntent(t *testing.T) {
	reg, gate, _ := newTestStack(t)
	tool := &mockTool{name: "bash", impact: 200}

	if err := simulateDispatch(reg, gate, tool, gates.Intent(200), -1); err != nil {
		t.Errorf("high-impact tool at high intent should pass: %v", err)
	}
}

// ── Scope cap enforcement ──

func TestEnforce_ScopeCapBlocksBelowIntent(t *testing.T) {
	reg, gate, _ := newTestStack(t)
	tool := &mockTool{name: "bash", impact: 200}

	// Request intent allows high, but scope caps at low
	err := simulateDispatch(reg, gate, tool, gates.Intent(200), 0)
	if err == nil {
		t.Error("scope cap of 0 should block a high-impact tool even at high intent")
	}
}

func TestEnforce_ScopeCapAllowsAtLimit(t *testing.T) {
	reg, gate, _ := newTestStack(t)
	tool := &mockTool{name: "file_write", impact: 100}

	// scope cap = 100, intent = 200. Tool impact = 100. Should pass.
	if err := simulateDispatch(reg, gate, tool, gates.Intent(200), 100); err != nil {
		t.Errorf("scope cap = tool impact should pass: %v", err)
	}
}

// ── Custom intent added at runtime ──

func TestEnforce_PinCapsHighImpactTool(t *testing.T) {
	reg, _, database := newTestStack(t)

	// Pin bash to mid (rank 100). Bash compiled impact is 200 (destructive).
	// Pin caps it to 100. At intent=0 it should block; at intent=100 it should pass.
	if err := database.SetToolIntent("bash", "mid"); err != nil {
		t.Fatalf("set tool intent: %v", err)
	}
	if err := reg.Load(database); err != nil {
		t.Fatalf("reload: %v", err)
	}

	tool := &mockTool{name: "bash", impact: 200} // compiled says destructive
	// At intent=0 — pinned impact is min(200, 100)=100, 100 > 0 → blocked
	err := simulateDispatch(reg, newTestGate(t, 200), tool, gates.Intent(0), -1)
	if err == nil {
		t.Error("pinned bash should be blocked at intent 0")
	}

	// At intent=100 — pinned impact is 100, 100 ≤ 100 → pass
	if err := simulateDispatch(reg, newTestGate(t, 200), tool, gates.Intent(100), -1); err != nil {
		t.Errorf("pinned bash should pass at intent 100: %v", err)
	}

	// Low-impact invocation (bash "ls") — compiled 0, pin doesn't raise, 0 ≤ 0 → pass
	toolLow := &mockTool{name: "bash", impact: 0}
	if err := simulateDispatch(reg, newTestGate(t, 200), toolLow, gates.Intent(0), -1); err != nil {
		t.Errorf("low-impact bash should pass even at intent 0: %v", err)
	}
}

func TestEnforce_PinWithCustomIntentBetweenSeededRanks(t *testing.T) {
	reg, gate, database := newTestStack(t)

	// Insert a custom intent at rank 50
	if err := database.CreateIntent(db.Intent{
		Name:              "between",
		Rank:              50,
		PromptDescription: "diagnostic, read-mostly",
	}); err != nil {
		t.Fatal(err)
	}
	// Pin net_info to "between" (rank 50). Compiled impact is 100.
	// Pin caps it to 50.
	if err := database.SetToolIntent("net_info", "between"); err != nil {
		t.Fatal(err)
	}
	_ = reg.Load(database)

	tool := &mockTool{name: "net_info", impact: 100}

	// At rank 0, pinned impact is min(100, 50)=50, 50 > 0 → blocked
	err := simulateDispatch(reg, gate, tool, gates.Intent(0), -1)
	if err == nil {
		t.Error("pinned tool should be blocked at rank 0")
	}

	// At rank 50 — pinned impact 50 ≤ 50 → pass
	if err := simulateDispatch(reg, gate, tool, gates.Intent(50), -1); err != nil {
		t.Errorf("pinned tool should pass at matching rank: %v", err)
	}

	// At rank 100 — pinned impact 50 ≤ 100 → pass
	if err := simulateDispatch(reg, gate, tool, gates.Intent(100), -1); err != nil {
		t.Errorf("pinned tool should pass at higher rank: %v", err)
	}
}

// ── Removing a tool override restores the Go default ──

func TestEnforce_RemovingOverrideRestoresDefault(t *testing.T) {
	reg, gate, database := newTestStack(t)

	// Pin bash to the lowest tier (artificially low)
	if err := database.SetToolIntent("bash", "low"); err != nil {
		t.Fatal(err)
	}
	_ = reg.Load(database)

	tool := &mockTool{name: "bash", impact: 200} // Go default is high
	// With the low pin, bash should pass even at rank 0
	if err := simulateDispatch(reg, gate, tool, gates.Intent(0), -1); err != nil {
		t.Errorf("bash pinned low should pass at rank 0: %v", err)
	}

	// Remove the override
	_ = database.DeleteToolIntent("bash")
	_ = reg.Load(database)

	// Now falls back to Go default (impact=2 → rank 200) and should be blocked at rank 0
	if err := simulateDispatch(reg, gate, tool, gates.Intent(0), -1); err == nil {
		t.Error("after removing pin, bash should be blocked at rank 0")
	}
}

// ── P0 regression: custom intent flows through the parser ──

// parseIntentLikeAPI mirrors the registry lookup in internal/api/api.go.
// If this test fails, the API parser's behavior has drifted from what the
// registry expects.
func parseIntentLikeAPI(reg *IntentRegistry, name string, fallback int) int {
	if name == "" {
		return fallback
	}
	if i, ok := reg.ByName(name); ok {
		return i.Rank
	}
	return fallback
}

// TestCustomIntentFlowsThroughAPIParser is the end-to-end P0 regression.
// Admin adds a custom intent, a request comes in with its name, the parser
// resolves it to the correct rank, and the gate honors that rank. Before
// the fix, custom names were silently rewritten by a hardcoded switch.
func TestCustomIntentFlowsThroughAPIParser(t *testing.T) {
	reg, gate, database := newTestStack(t)
	fallback := 0

	// Admin creates "between" at rank 50 — between the seeded 0 and 100.
	if err := database.CreateIntent(db.Intent{
		Name: "between", Rank: 50, PromptDescription: "diagnostic read",
	}); err != nil {
		t.Fatal(err)
	}
	// Admin creates "topmost" at rank 300 — above the seeded 200.
	if err := database.CreateIntent(db.Intent{
		Name: "topmost", Rank: 300, PromptDescription: "destructive termination",
	}); err != nil {
		t.Fatal(err)
	}
	_ = reg.Load(database)

	// 1. Custom name resolves to its own rank, not collapsed to a seeded rank
	if got := parseIntentLikeAPI(reg, "between", fallback); got != 50 {
		t.Errorf("parse(\"between\") = %d, want 50 — P0 regression: custom intents being collapsed", got)
	}
	// 2. Custom high name resolves correctly
	if got := parseIntentLikeAPI(reg, "topmost", fallback); got != 300 {
		t.Errorf("parse(\"topmost\") = %d, want 300", got)
	}
	// 3. Seeded name still resolves via registry
	if got := parseIntentLikeAPI(reg, "mid", fallback); got != 100 {
		t.Errorf("parse(\"mid\") = %d, want 100", got)
	}
	// 4. Unknown garbage returns the safe default
	if got := parseIntentLikeAPI(reg, "random_nonsense", fallback); got != fallback {
		t.Errorf("parse(\"random_nonsense\") = %d, want fallback %d", got, fallback)
	}

	// Now verify the resolved rank is enforced at the gate end-to-end:
	// a bash tool pinned to "between" should block at rank 0 and pass at
	// rank 50, rank 100, and rank 300.
	_ = database.SetToolIntent("bash", "between")
	_ = reg.Load(database)

	tool := &mockTool{name: "bash", impact: 200}

	if err := simulateDispatch(reg, gate, tool, gates.Intent(parseIntentLikeAPI(reg, "low", fallback)), -1); err == nil {
		t.Error("between-pinned bash should block at lowest tier")
	}
	if err := simulateDispatch(reg, gate, tool, gates.Intent(parseIntentLikeAPI(reg, "between", fallback)), -1); err != nil {
		t.Errorf("between-pinned bash should pass at matching rank: %v", err)
	}
	if err := simulateDispatch(reg, gate, tool, gates.Intent(parseIntentLikeAPI(reg, "mid", fallback)), -1); err != nil {
		t.Errorf("between-pinned bash should pass at mid rank: %v", err)
	}
	if err := simulateDispatch(reg, gate, tool, gates.Intent(parseIntentLikeAPI(reg, "topmost", fallback)), -1); err != nil {
		t.Errorf("between-pinned bash should pass at topmost rank: %v", err)
	}
}

func TestPromptBlockIncludesCustomIntent(t *testing.T) {
	reg, _, database := newTestStack(t)

	_ = database.CreateIntent(db.Intent{
		Name:              "between",
		Rank:              50,
		PromptDescription: "diagnostic monitoring",
	})
	_ = reg.Load(database)

	block := reg.PromptBlock(-1)
	if !containsString(block, "between") {
		t.Errorf("PromptBlock missing between: %s", block)
	}
	if !containsString(block, "diagnostic monitoring") {
		t.Errorf("PromptBlock missing between description: %s", block)
	}
}

func containsString(s, sub string) bool {
	return len(sub) == 0 || len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
