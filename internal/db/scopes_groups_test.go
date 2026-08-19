package db

import "testing"

// Belonging to a group grants what the group holds.
//
// It did not. This package stored which groups a user was in, stored which permissions
// a group held, and served pages for an administrator to set both — and then resolved a
// user's permissions from their own list alone. So somebody put in a group full of
// permissions received none of them, and nothing said why.
//
// The rule now lives in the permissions package, which an application embedding this one
// can also call, so the two cannot drift apart again. These tests are here rather than
// there because what they check is that this package hands the rule the right values.

func TestAUserInAGroupGetsWhatTheGroupHolds(t *testing.T) {
	d := openTestDB(t)

	if err := d.CreateScope(Scope{
		Name: "reading", Tools: []string{"file_read", "process_list"}, IntentCap: 100,
	}); err != nil {
		t.Fatalf("create scope: %v", err)
	}
	if err := d.CreateGroup(Group{Name: "operators", Scopes: []string{"reading"}}); err != nil {
		t.Fatalf("create group: %v", err)
	}

	// She holds nothing directly — everything must come through the group.
	user := &User{Username: "alice", Groups: []string{"operators"}, MaxIntent: 200}

	got, err := d.ResolveUserScope(user)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !got.AllowedTools["file_read"] || !got.AllowedTools["process_list"] {
		t.Errorf("her group's tools did not reach her: %v", got.AllowedTools)
	}
	if got.MaxIntent != 100 {
		t.Errorf("reach is %d, want 100 — the ceiling her group's scope allows", got.MaxIntent)
	}
}

// A scope's ceiling is read from the row, and the user's own still caps it.
func TestAScopesCeilingIsReadAndTheUsersOwnStillCaps(t *testing.T) {
	d := openTestDB(t)

	if err := d.CreateScope(Scope{
		Name: "shell", Tools: []string{"bash"}, IntentCap: 200,
	}); err != nil {
		t.Fatalf("create scope: %v", err)
	}

	high, err := d.ResolveUserScope(&User{Username: "alice", Scopes: []string{"shell"}, MaxIntent: 200})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if high.MaxIntent != 200 {
		t.Errorf("reach is %d, want 200 — the scope allows it and so does she", high.MaxIntent)
	}

	low, err := d.ResolveUserScope(&User{Username: "bob", Scopes: []string{"shell"}, MaxIntent: 100})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if low.MaxIntent != 100 {
		t.Errorf("reach is %d, want 100 — his own ceiling is the lower of the two", low.MaxIntent)
	}
}

// A scope written and read back keeps its ceiling.
//
// The column was added by this change; a scope stored without it reading back as zero
// would take everything away from whoever holds it.
func TestAScopesCeilingSurvivesBeingStored(t *testing.T) {
	d := openTestDB(t)

	if err := d.CreateScope(Scope{Name: "shell", Tools: []string{"bash"}, IntentCap: 200}); err != nil {
		t.Fatalf("create: %v", err)
	}

	one, err := d.GetScope("shell")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if one.IntentCap != 200 {
		t.Errorf("read back with ceiling %d, want 200", one.IntentCap)
	}

	all, err := d.ListScopes()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, s := range all {
		if s.Name == "shell" && s.IntentCap != 200 {
			t.Errorf("listed with ceiling %d, want 200", s.IntentCap)
		}
	}

	if err := d.UpdateScope("shell", Scope{
		Name: "shell", Tools: []string{"bash"}, IntentCap: 100,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	after, err := d.GetScope("shell")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if after.IntentCap != 100 {
		t.Errorf("after lowering it, the ceiling is %d, want 100", after.IntentCap)
	}
}

// The seeded read-only scope reaches nothing that changes anything.
func TestTheSeededReadOnlyScopeReachesNothingThatChangesAnything(t *testing.T) {
	d := openTestDB(t)
	if err := d.SeedDefaultScopes(); err != nil {
		t.Fatalf("seed: %v", err)
	}

	readonly, err := d.GetScope("readonly")
	if err != nil {
		t.Fatalf("get readonly: %v", err)
	}
	if readonly.IntentCap != 0 {
		t.Errorf("the read-only scope allows a reach of %d, want 0 — read-only means "+
			"read-only whatever tools are later added to its list", readonly.IntentCap)
	}

	admin, err := d.GetScope("admin")
	if err != nil {
		t.Fatalf("get admin: %v", err)
	}
	if admin.IntentCap != 200 {
		t.Errorf("the admin scope allows a reach of %d, want 200", admin.IntentCap)
	}
}
