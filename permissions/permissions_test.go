package permissions

import (
	"reflect"
	"testing"
)

// What a person may do, worked out from what they hold.
//
// These are values in and a value out — no database, no fixture, nothing to set up. That
// is the reason the rule was pulled out of the two database packages that each had a
// copy of it: a rule about values is testable as values, and neither copy was.

// scopesByName and groupsByName save repeating the map building in every case.
func scopesByName(s ...Scope) map[string]Scope {
	out := map[string]Scope{}
	for _, x := range s {
		out[x.Name] = x
	}
	return out
}

func groupsByName(g ...Group) map[string]Group {
	out := map[string]Group{}
	for _, x := range g {
		out[x.Name] = x
	}
	return out
}

// Someone with nothing gets nothing.
func TestAUserWithNoScopesAndNoGroupsMayDoNothing(t *testing.T) {
	got := Resolve(User{Username: "alice", MaxIntent: 200}, nil, nil)

	if len(got.AllowedTools) != 0 {
		t.Errorf("allowed %d tools, want none: %v", len(got.AllowedTools), got.AllowedTools)
	}
	if got.MaxIntent != 0 {
		t.Errorf("reach is %d, want 0 — someone granted nothing must not inherit a ceiling",
			got.MaxIntent)
	}
	if got.Username != "alice" {
		t.Errorf("username is %q", got.Username)
	}
}

// A group grants what it holds. This is the difference the two copies disagreed about.
func TestAGroupGrantsItsScopes(t *testing.T) {
	scopes := scopesByName(Scope{Name: "reading", Tools: []string{"read_files", "list_processes"}, IntentCap: 100})
	groups := groupsByName(Group{Name: "operators", Scopes: []string{"reading"}})

	// She holds nothing directly. Everything she gets comes through the group.
	got := Resolve(User{Username: "alice", Groups: []string{"operators"}, MaxIntent: 200}, scopes, groups)

	if !got.AllowedTools["read_files"] || !got.AllowedTools["list_processes"] {
		t.Errorf("her group's tools did not reach her: %v", got.AllowedTools)
	}
	if got.MaxIntent != 100 {
		t.Errorf("reach is %d, want 100 — the ceiling her group's scope allows", got.MaxIntent)
	}
}

// Tools from her own scopes and her groups' scopes are added together.
func TestToolsFromScopesAndGroupsAreAddedTogether(t *testing.T) {
	scopes := scopesByName(
		Scope{Name: "reading", Tools: []string{"read_files"}, IntentCap: 100},
		Scope{Name: "listing", Tools: []string{"list_processes"}, IntentCap: 100},
	)
	groups := groupsByName(Group{Name: "operators", Scopes: []string{"listing"}})

	got := Resolve(User{
		Username: "alice", Scopes: []string{"reading"}, Groups: []string{"operators"}, MaxIntent: 200,
	}, scopes, groups)

	want := map[string]bool{"read_files": true, "list_processes": true}
	if !reflect.DeepEqual(got.AllowedTools, want) {
		t.Errorf("allowed %v, want %v", got.AllowedTools, want)
	}
}

// Where two scopes set a ceiling for the same tool, the lower one holds.
//
// Holding more scopes widens which tools are reachable; it must never raise how far any
// one of them may go.
func TestTheLowestCeilingForAToolWins(t *testing.T) {
	scopes := scopesByName(
		Scope{Name: "wide", Tools: []string{"bash"}, Cap: map[string]int{"bash": 200}, IntentCap: 200},
		Scope{Name: "narrow", Tools: []string{"bash"}, Cap: map[string]int{"bash": 100}, IntentCap: 200},
	)

	got := Resolve(User{Username: "alice", Scopes: []string{"wide", "narrow"}, MaxIntent: 200}, scopes, nil)

	if got.MaxImpact["bash"] != 100 {
		t.Errorf("the ceiling for bash is %d, want 100 — holding a wider scope raised it",
			got.MaxImpact["bash"])
	}
}

// Where scopes disagree about how far a run may go, the most permissive holds — and the
// user's own ceiling still caps it.
func TestReachIsTheHighestScopeAllowsThenTheUsersOwnCeiling(t *testing.T) {
	scopes := scopesByName(
		Scope{Name: "low", Tools: []string{"read_files"}, IntentCap: 0},
		Scope{Name: "high", Tools: []string{"bash"}, IntentCap: 200},
	)

	// Her scopes allow 200 between them, and she may have it.
	got := Resolve(User{Username: "alice", Scopes: []string{"low", "high"}, MaxIntent: 200}, scopes, nil)
	if got.MaxIntent != 200 {
		t.Errorf("reach is %d, want 200 — the highest her scopes allow", got.MaxIntent)
	}

	// The same scopes, but she is capped lower herself.
	got = Resolve(User{Username: "bob", Scopes: []string{"low", "high"}, MaxIntent: 100}, scopes, nil)
	if got.MaxIntent != 100 {
		t.Errorf("reach is %d, want 100 — his own ceiling is lower than his scopes allow",
			got.MaxIntent)
	}
}

// A name with nothing behind it grants nothing, and does not stop the rest working.
func TestUnknownScopeAndGroupNamesAreIgnored(t *testing.T) {
	scopes := scopesByName(Scope{Name: "reading", Tools: []string{"read_files"}, IntentCap: 100})
	groups := groupsByName(Group{Name: "operators", Scopes: []string{"reading", "deleted-scope"}})

	got := Resolve(User{
		Username:  "alice",
		Scopes:    []string{"reading", "another-deleted-scope"},
		Groups:    []string{"operators", "deleted-group"},
		MaxIntent: 200,
	}, scopes, groups)

	if !got.AllowedTools["read_files"] {
		t.Errorf("a deleted name stopped a good one working: %v", got.AllowedTools)
	}
	if len(got.AllowedTools) != 1 {
		t.Errorf("allowed %v, want only read_files", got.AllowedTools)
	}
}

// Every name having nothing behind it is the same as holding nothing.
func TestScopesThatAllVanishedLeaveNothing(t *testing.T) {
	got := Resolve(User{
		Username: "alice", Scopes: []string{"gone"}, Groups: []string{"also-gone"}, MaxIntent: 200,
	}, scopesByName(), groupsByName())

	if len(got.AllowedTools) != 0 {
		t.Errorf("allowed %v, want nothing", got.AllowedTools)
	}
	if got.MaxIntent != 0 {
		t.Errorf("reach is %d, want 0 — nothing granted must not leave her ceiling standing",
			got.MaxIntent)
	}
}

// Holding the same scope twice changes nothing.
func TestHoldingAScopeTwiceChangesNothing(t *testing.T) {
	scopes := scopesByName(Scope{
		Name: "reading", Tools: []string{"read_files"},
		Cap: map[string]int{"read_files": 100}, IntentCap: 100,
	})
	groups := groupsByName(Group{Name: "operators", Scopes: []string{"reading"}})

	once := Resolve(User{Username: "alice", Scopes: []string{"reading"}, MaxIntent: 200}, scopes, groups)
	twice := Resolve(User{
		Username: "alice", Scopes: []string{"reading"}, Groups: []string{"operators"}, MaxIntent: 200,
	}, scopes, groups)

	if !reflect.DeepEqual(once, twice) {
		t.Errorf("granted directly: %+v\ngranted twice:    %+v", once, twice)
	}
}

// "*" means every tool, and is carried through as itself.
//
// The gate reads it: an allowed set holding "*" permits anything. Turning it into a list
// here would need this package to know every tool that exists, which it does not.
func TestEveryToolIsCarriedThroughAsItself(t *testing.T) {
	scopes := scopesByName(Scope{Name: "everything", Tools: []string{"*"}, IntentCap: 200})

	got := Resolve(User{Username: "alice", Scopes: []string{"everything"}, MaxIntent: 200}, scopes, nil)

	if !got.AllowedTools["*"] {
		t.Errorf("the everything mark did not survive: %v", got.AllowedTools)
	}
}

// The maps handed back are always usable, never nil.
//
// A caller writing into one, or reading a missing key, must not have to check first.
func TestTheAnswerIsAlwaysUsable(t *testing.T) {
	got := Resolve(User{Username: "alice"}, nil, nil)

	if got.AllowedTools == nil {
		t.Error("the allowed tools map is nil")
	}
	if got.MaxImpact == nil {
		t.Error("the per-tool ceiling map is nil")
	}
	if got.MaxImpact["anything"] != 0 {
		t.Error("reading a missing tool's ceiling did not give zero")
	}
}
