package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
	"github.com/Compdeep/kaiju/internal/db"
)

// mockTool is a minimal Tool for testing. Its Impact is configurable via
// the impact field (returned regardless of params).
type mockTool struct {
	name   string
	impact int
}

func (m *mockTool) Name() string                { return m.name }
func (m *mockTool) Description() string         { return "mock tool for testing" }
func (m *mockTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (m *mockTool) Impact(_ map[string]any) int { return m.impact }
func (m *mockTool) Execute(_ context.Context, _ map[string]any) (string, error) {
	return "ok", nil
}

// Compile-time interface check
var _ agenttools.Tool = (*mockTool)(nil)

// testLadder is a three-tier intent fixture used across registry tests.
// The names are deliberately generic — Go production code has no
// knowledge of specific intent names and tests should prove that by
// working with any arbitrary ladder.
var testLadder = []db.Intent{
	{Name: "low", Rank: 0, Description: "read-only", PromptDescription: "read-only", IsBuiltin: true},
	{Name: "mid", Rank: 100, Description: "normal", PromptDescription: "normal", IsBuiltin: true, IsDefault: true},
	{Name: "high", Rank: 200, Description: "destructive", PromptDescription: "destructive", IsBuiltin: true},
}

// newTestRegistry opens a fresh DB, seeds the three-tier ladder, and
// returns a loaded IntentRegistry.
func newTestRegistry(t *testing.T) (*IntentRegistry, *db.DB) {
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
	return reg, database
}

func TestRegistryLoadsSeeds(t *testing.T) {
	reg, _ := newTestRegistry(t)
	intents := reg.List()
	if len(intents) != 3 {
		t.Fatalf("expected 3 loaded, got %d", len(intents))
	}
	// Ordered ascending by rank
	if intents[0].Name != "low" || intents[1].Name != "mid" || intents[2].Name != "high" {
		t.Errorf("order wrong: %v", intents)
	}
}

func TestRegistryByName(t *testing.T) {
	reg, _ := newTestRegistry(t)
	i, ok := reg.ByName("mid")
	if !ok {
		t.Fatal("mid not found")
	}
	if i.Rank != 100 {
		t.Errorf("mid rank = %d, want 100", i.Rank)
	}

	// Case insensitivity
	_, ok = reg.ByName("LOW")
	if !ok {
		t.Error("ByName should be case insensitive")
	}

	// Unknown
	_, ok = reg.ByName("nonexistent")
	if ok {
		t.Error("unknown should return false")
	}
}

func TestRegistryRankByName(t *testing.T) {
	reg, _ := newTestRegistry(t)
	if r := reg.RankByName("high"); r != 200 {
		t.Errorf("high rank = %d, want 200", r)
	}
	if r := reg.RankByName("nope"); r != -1 {
		t.Errorf("unknown should return -1, got %d", r)
	}
}

func TestRegistryNameByRank(t *testing.T) {
	reg, _ := newTestRegistry(t)
	if n := reg.NameByRank(0); n != "low" {
		t.Errorf("rank 0 = %q, want low", n)
	}
	if n := reg.NameByRank(100); n != "mid" {
		t.Errorf("rank 100 = %q, want mid", n)
	}
	if n := reg.NameByRank(99); n != "" {
		t.Errorf("rank 99 should be unknown, got %q", n)
	}
}

func TestRegistryListAllowed(t *testing.T) {
	reg, _ := newTestRegistry(t)

	// maxRank=100 → low + mid only
	allowed := reg.ListAllowed(100)
	if len(allowed) != 2 {
		t.Errorf("maxRank=100 should allow 2, got %d", len(allowed))
	}
	for _, i := range allowed {
		if i.Rank > 100 {
			t.Errorf("intent %q rank %d exceeds cap 100", i.Name, i.Rank)
		}
	}

	// maxRank=-1 → all
	if len(reg.ListAllowed(-1)) != 3 {
		t.Error("maxRank=-1 should return all")
	}

	// maxRank=0 → only the lowest
	allowed = reg.ListAllowed(0)
	if len(allowed) != 1 || allowed[0].Name != "low" {
		t.Errorf("maxRank=0 should return only the lowest rank, got %v", allowed)
	}
}

func TestRegistryPromptBlock(t *testing.T) {
	reg, _ := newTestRegistry(t)
	block := reg.PromptBlock(-1)
	for _, name := range []string{"low", "mid", "high"} {
		if !strings.Contains(block, "\""+name+"\"") {
			t.Errorf("PromptBlock missing %q", name)
		}
	}
	// Filtered by scope
	block = reg.PromptBlock(0)
	if strings.Contains(block, "mid") || strings.Contains(block, "high") {
		t.Errorf("PromptBlock(0) leaked higher intents: %s", block)
	}
}

func TestRegistryAllowedNames(t *testing.T) {
	reg, _ := newTestRegistry(t)
	names := reg.AllowedNames(-1)
	if len(names) != 3 || names[0] != "low" || names[2] != "high" {
		t.Errorf("AllowedNames = %v", names)
	}
}

func TestResolveToolIntentFallback(t *testing.T) {
	// No DB pin → falls back to tool.Impact(), which already returns a rank
	// on the same scale as the registry. No translation happens.
	reg, _ := newTestRegistry(t)

	tests := []struct {
		name     string
		impact   int
		wantRank int
	}{
		{"low_tool", 0, 0},
		{"mid_tool", 100, 100},
		{"high_tool", 200, 200},
		{"custom_rank_tool", 50, 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := &mockTool{name: tt.name, impact: tt.impact}
			got := reg.ResolveToolIntent(tt.name, tool, nil)
			if got != tt.wantRank {
				t.Errorf("impact %d → rank %d, want %d", tt.impact, got, tt.wantRank)
			}
		})
	}
}

func TestResolveToolIntentWithPin(t *testing.T) {
	reg, database := newTestRegistry(t)

	// Pin bash to mid (rank 100). Compiled impact is 200 (destructive).
	// Pin is a ceiling — compiled 200 gets capped to 100.
	_ = database.SetToolIntent("bash", "mid")
	_ = reg.Load(database)

	tool := &mockTool{name: "bash", impact: 200}
	got := reg.ResolveToolIntent("bash", tool, nil)
	if got != 100 {
		t.Errorf("pin should cap: got %d, want 100", got)
	}

	// Same tool, low-impact invocation. Compiled 0 stays 0 (pin doesn't raise).
	toolLow := &mockTool{name: "bash", impact: 0}
	got = reg.ResolveToolIntent("bash", toolLow, nil)
	if got != 0 {
		t.Errorf("pin should not raise low impact: got %d, want 0", got)
	}
}

func TestResolveToolIntentCustomIntent(t *testing.T) {
	reg, database := newTestRegistry(t)

	// Insert a custom intent between low and mid
	_ = database.CreateIntent(db.Intent{
		Name:              "between",
		Rank:              50,
		PromptDescription: "diagnostic",
	})
	// Pin net_info to "between" (rank 50). Compiled impact is 100.
	// Pin caps it to 50.
	_ = database.SetToolIntent("net_info", "between")
	_ = reg.Load(database)

	tool := &mockTool{name: "net_info", impact: 100}
	got := reg.ResolveToolIntent("net_info", tool, nil)
	if got != 50 {
		t.Errorf("custom intent pin should cap: got %d, want 50", got)
	}

	// Low-impact invocation stays low
	toolLow := &mockTool{name: "net_info", impact: 0}
	got = reg.ResolveToolIntent("net_info", toolLow, nil)
	if got != 0 {
		t.Errorf("pin should not raise low impact: got %d, want 0", got)
	}

	// Verify the intent is in the registry
	i, ok := reg.ByName("between")
	if !ok || i.Rank != 50 {
		t.Errorf("custom intent not in registry: %v %v", i, ok)
	}
}

func TestResolveToolIntentUnknownOverrideFallsBack(t *testing.T) {
	reg, database := newTestRegistry(t)

	database.SetToolIntent("weird_tool", "nonexistent_intent")
	_ = reg.Load(database)

	tool := &mockTool{name: "weird_tool", impact: 100} // compiled default = rank 100
	got := reg.ResolveToolIntent("weird_tool", tool, nil)
	if got != 100 {
		t.Errorf("unknown pin should fall back to compiled default; got %d, want 100", got)
	}
}

func TestRegistryMaxRank(t *testing.T) {
	reg, database := newTestRegistry(t)
	if r := reg.MaxRank(); r != 200 {
		t.Errorf("default max rank = %d, want 200", r)
	}

	_ = database.CreateIntent(db.Intent{Name: "topmost", Rank: 300, PromptDescription: "..."})
	_ = reg.Load(database)
	if r := reg.MaxRank(); r != 300 {
		t.Errorf("after adding topmost=300, max = %d, want 300", r)
	}
}

func TestRegistryDefaultRank(t *testing.T) {
	reg, _ := newTestRegistry(t)
	// "mid" is flagged IsDefault=true in the fixture, so DefaultRank must
	// return its rank regardless of list position.
	if r := reg.DefaultRank(); r != 100 {
		t.Errorf("DefaultRank = %d, want 100", r)
	}
}

func TestRegistryDefaultRankStableWithCustomIntents(t *testing.T) {
	// Adding custom intents biased to one end of the ladder must NOT shift
	// the default rank — the flag is what matters, not list position.
	reg, database := newTestRegistry(t)
	// Add two custom intents below the default (biasing "middle position" downward)
	_ = database.CreateIntent(db.Intent{Name: "c1", Rank: 10, PromptDescription: "..."})
	_ = database.CreateIntent(db.Intent{Name: "c2", Rank: 20, PromptDescription: "..."})
	_ = reg.Load(database)

	// If DefaultRank used list position (len/2), adding two low-end intents
	// would shift it. With the flag, it stays at 100.
	if r := reg.DefaultRank(); r != 100 {
		t.Errorf("DefaultRank shifted to %d after adding custom intents; should stay at 100", r)
	}
}
