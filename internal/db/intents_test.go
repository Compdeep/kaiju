package db

import (
	"path/filepath"
	"testing"
)

// openTestDB creates a fresh SQLite file in a temp directory for each test.
// The DB starts with no intents — seeding is the caller's responsibility.
func openTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// seedThreeTier populates the DB with a three-tier test ladder. Test fixture
// names are deliberately arbitrary — Go production code never references
// any specific intent name.
func seedThreeTier(t *testing.T, d *DB) {
	t.Helper()
	seeds := []Intent{
		{Name: "low", Rank: 0, Description: "read-only", PromptDescription: "read-only", IsBuiltin: true},
		{Name: "mid", Rank: 100, Description: "normal work", PromptDescription: "normal work", IsBuiltin: true, IsDefault: true},
		{Name: "high", Rank: 200, Description: "destructive", PromptDescription: "destructive", IsBuiltin: true},
	}
	if err := d.SeedIntentsFromConfig(seeds); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestFreshDBHasNoIntents(t *testing.T) {
	d := openTestDB(t)
	intents, err := d.ListIntents()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(intents) != 0 {
		t.Errorf("fresh DB should have 0 intents, got %d — seeds should come from config, not Go", len(intents))
	}
}

func TestSeedFromConfig(t *testing.T) {
	d := openTestDB(t)
	seedThreeTier(t, d)

	intents, err := d.ListIntents()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(intents) != 3 {
		t.Fatalf("expected 3 seeded intents, got %d", len(intents))
	}

	want := map[string]int{"low": 0, "mid": 100, "high": 200}
	for _, i := range intents {
		wantRank, ok := want[i.Name]
		if !ok {
			t.Errorf("unexpected intent %q", i.Name)
			continue
		}
		if i.Rank != wantRank {
			t.Errorf("intent %q rank = %d, want %d", i.Name, i.Rank, wantRank)
		}
		if !i.IsBuiltin {
			t.Errorf("intent %q should be builtin (seeded with builtin=true)", i.Name)
		}
	}
}

func TestSeedIdempotent(t *testing.T) {
	d := openTestDB(t)
	seedThreeTier(t, d)
	seedThreeTier(t, d) // second call must be a no-op
	intents, _ := d.ListIntents()
	if len(intents) != 3 {
		t.Errorf("after second seed, got %d intents, want 3", len(intents))
	}
}

func TestCreateCustomIntent(t *testing.T) {
	d := openTestDB(t)
	seedThreeTier(t, d)
	custom := Intent{
		Name:              "between",
		Rank:              50,
		Description:       "custom tier",
		PromptDescription: "custom tier",
		IsBuiltin:         false,
	}
	if err := d.CreateIntent(custom); err != nil {
		t.Fatalf("create: %v", err)
	}

	intents, _ := d.ListIntents()
	if len(intents) != 4 {
		t.Fatalf("expected 4 after custom add, got %d", len(intents))
	}

	// Verify ordering by rank ascending
	want := []string{"low", "between", "mid", "high"}
	for i, n := range want {
		if intents[i].Name != n {
			t.Errorf("intents[%d] = %q, want %q", i, intents[i].Name, n)
		}
	}

	got, err := d.GetIntent("between")
	if err != nil {
		t.Fatalf("get between: %v", err)
	}
	if got.Rank != 50 {
		t.Errorf("rank = %d, want 50", got.Rank)
	}
	if got.IsBuiltin {
		t.Errorf("custom intent should not be builtin")
	}
}

func TestCreateDuplicateIntent(t *testing.T) {
	d := openTestDB(t)
	seedThreeTier(t, d)
	if err := d.CreateIntent(Intent{Name: "low", Rank: 99}); err == nil {
		t.Error("expected error creating duplicate, got nil")
	}
}

func TestCannotDeleteBuiltin(t *testing.T) {
	d := openTestDB(t)
	seedThreeTier(t, d)
	for _, name := range []string{"low", "mid", "high"} {
		if err := d.DeleteIntent(name); err == nil {
			t.Errorf("expected error deleting builtin %q, got nil", name)
		}
	}
	intents, _ := d.ListIntents()
	if len(intents) != 3 {
		t.Errorf("builtins were deleted: got %d, want 3", len(intents))
	}
}

func TestDeleteCustomIntent(t *testing.T) {
	d := openTestDB(t)
	seedThreeTier(t, d)
	_ = d.CreateIntent(Intent{Name: "between", Rank: 50, PromptDescription: "..."})
	if err := d.DeleteIntent("between"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := d.GetIntent("between"); err == nil {
		t.Error("intent still exists after delete")
	}
}

func TestDeleteIntentCascadesToolOverrides(t *testing.T) {
	d := openTestDB(t)
	seedThreeTier(t, d)
	_ = d.CreateIntent(Intent{Name: "nuke", Rank: 300, PromptDescription: "..."})
	_ = d.SetToolIntent("bash", "nuke")
	_ = d.DeleteIntent("nuke")

	// Tool override should be gone
	if _, err := d.GetToolIntent("bash"); err == nil {
		t.Error("tool override should be removed when intent is deleted")
	}
}

func TestUpdateBuiltinDescriptionsOnly(t *testing.T) {
	d := openTestDB(t)
	seedThreeTier(t, d)
	// Passing matching rank means "not changing rank" — descriptions still update.
	err := d.UpdateIntent("low", Intent{
		Rank:              0,
		Description:       "new desc",
		PromptDescription: "new prompt desc",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := d.GetIntent("low")
	if got.Rank != 0 {
		t.Errorf("rank changed to %d; should stay at 0", got.Rank)
	}
	if got.Description != "new desc" {
		t.Errorf("description not updated: %q", got.Description)
	}
}

func TestUpdateBuiltinRejectsRankChange(t *testing.T) {
	d := openTestDB(t)
	seedThreeTier(t, d)
	err := d.UpdateIntent("low", Intent{
		Rank:        999,
		Description: "hack attempt",
	})
	if err == nil {
		t.Fatal("expected error when changing builtin rank, got nil")
	}
	got, _ := d.GetIntent("low")
	if got.Rank != 0 {
		t.Errorf("rank was changed despite error: %d", got.Rank)
	}
	if got.Description == "hack attempt" {
		t.Error("description was updated even though rank change failed — should be all-or-nothing")
	}
}

func TestUpdateCustomIntent(t *testing.T) {
	d := openTestDB(t)
	seedThreeTier(t, d)
	_ = d.CreateIntent(Intent{Name: "between", Rank: 50, PromptDescription: "old"})

	err := d.UpdateIntent("between", Intent{
		Rank:              60,
		Description:       "updated",
		PromptDescription: "updated prompt",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := d.GetIntent("between")
	if got.Rank != 60 {
		t.Errorf("rank = %d, want 60", got.Rank)
	}
}

func TestToolIntentCRUD(t *testing.T) {
	d := openTestDB(t)
	seedThreeTier(t, d)

	// No override initially
	if _, err := d.GetToolIntent("bash"); err == nil {
		t.Error("expected no override for bash initially")
	}

	// Set
	if err := d.SetToolIntent("bash", "high"); err != nil {
		t.Fatalf("set: %v", err)
	}
	name, err := d.GetToolIntent("bash")
	if err != nil || name != "high" {
		t.Errorf("after set, got %q (%v), want high", name, err)
	}

	// List
	all, _ := d.ListToolIntents()
	if all["bash"] != "high" {
		t.Errorf("ListToolIntents[bash] = %q", all["bash"])
	}

	// Replace
	_ = d.SetToolIntent("bash", "mid")
	name, _ = d.GetToolIntent("bash")
	if name != "mid" {
		t.Errorf("after replace, got %q, want mid", name)
	}

	// Delete
	_ = d.DeleteToolIntent("bash")
	if _, err := d.GetToolIntent("bash"); err == nil {
		t.Error("bash override still exists after delete")
	}
}
