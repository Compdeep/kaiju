package agent

import (
	"testing"

	"github.com/Compdeep/kaiju/permissions"
)

// What the permissions package decides is what the gate enforces.
//
// Those are two packages that never call each other: permissions works out an answer from
// values, and the gate reads an answer off a Trigger. An application carries it from one
// to the other. So a test of either alone proves nothing about the pair, and this is the
// one that joins them — the answer is produced by the real function and handed to the
// real check.
//
// The conversion in the middle is the four lines every application writes. If those four
// lines ever go wrong, this is what fails.

// asScope converts a decided answer into what a Trigger carries.
func asScope(g permissions.Grant) *ResolvedScope {
	return &ResolvedScope{
		Username:     g.Username,
		AllowedTools: g.AllowedTools,
		MaxImpact:    g.MaxImpact,
		MaxIntent:    g.MaxIntent,
	}
}

// A tool a person's permissions allow passes the gate; one they do not is refused.
func TestTheGateAllowsWhatThePermissionsAllow(t *testing.T) {
	scopes := map[string]permissions.Scope{
		"reading": {Name: "reading", Tools: []string{"file_read"}, IntentCap: 0},
	}

	scope := asScope(permissions.Resolve(
		permissions.User{Username: "alice", Scopes: []string{"reading"}, MaxIntent: 200},
		scopes, nil,
	))

	if err := scopeAllows("file_read", scope); err != nil {
		t.Errorf("a tool her permissions allow was refused: %v", err)
	}
	if err := scopeAllows("process_kill", scope); err == nil {
		t.Error("a tool her permissions do not allow passed the gate")
	}
}

// A tool reaching her only through a group passes the gate.
//
// This is the case that was broken: the permissions side ignored groups, so the gate was
// being handed an empty list and refusing everything for anyone whose access came that
// way.
func TestTheGateAllowsWhatAGroupGave(t *testing.T) {
	scopes := map[string]permissions.Scope{
		"reading": {Name: "reading", Tools: []string{"file_read"}, IntentCap: 0},
	}
	groups := map[string]permissions.Group{
		"operators": {Name: "operators", Scopes: []string{"reading"}},
	}

	// Nothing of her own — everything through the group.
	scope := asScope(permissions.Resolve(
		permissions.User{Username: "alice", Groups: []string{"operators"}, MaxIntent: 200},
		scopes, groups,
	))

	if err := scopeAllows("file_read", scope); err != nil {
		t.Errorf("a tool her group gave her was refused at the gate: %v", err)
	}
}

// Somebody with nothing is refused everything, rather than allowed everything.
//
// The gate treats a nil scope as unrestricted, which is right for a local operator with
// no permissions system at all. A resolved answer that happens to be empty must not
// arrive as nil, or the person with the fewest permissions would have the most.
func TestSomebodyWithNothingIsRefusedRatherThanUnrestricted(t *testing.T) {
	scope := asScope(permissions.Resolve(
		permissions.User{Username: "alice", MaxIntent: 200}, nil, nil,
	))

	if scope == nil {
		t.Fatal("an empty answer converted to nil, which the gate reads as unrestricted")
	}
	for _, tool := range []string{"file_read", "process_kill", "bash"} {
		if err := scopeAllows(tool, scope); err == nil {
			t.Errorf("%s passed the gate for somebody granted nothing", tool)
		}
	}
}

// The everything mark passes the gate for every tool.
func TestTheEverythingMarkPassesTheGate(t *testing.T) {
	scopes := map[string]permissions.Scope{
		"admin": {Name: "admin", Tools: []string{"*"}, IntentCap: 200},
	}

	scope := asScope(permissions.Resolve(
		permissions.User{Username: "root", Scopes: []string{"admin"}, MaxIntent: 200},
		scopes, nil,
	))

	for _, tool := range []string{"file_read", "process_kill", "a_tool_nobody_has_written"} {
		if err := scopeAllows(tool, scope); err != nil {
			t.Errorf("%s was refused despite the everything mark: %v", tool, err)
		}
	}
}

// The per-tool ceiling the permissions decided is the one the gate is given.
//
// The gate reads MaxImpact by tool name and hands it to the triad check as the scope's
// cap. What is asserted here is that the number arrives — the triad check itself has its
// own tests.
func TestThePerToolCeilingArrivesAtTheGate(t *testing.T) {
	scopes := map[string]permissions.Scope{
		"wide":   {Name: "wide", Tools: []string{"bash"}, Cap: map[string]int{"bash": 200}, IntentCap: 200},
		"narrow": {Name: "narrow", Tools: []string{"bash"}, Cap: map[string]int{"bash": 100}, IntentCap: 200},
	}

	scope := asScope(permissions.Resolve(
		permissions.User{Username: "alice", Scopes: []string{"wide", "narrow"}, MaxIntent: 200},
		scopes, nil,
	))

	cap, set := scope.MaxImpact["bash"]
	if !set {
		t.Fatal("no ceiling for bash arrived, so the gate would apply none")
	}
	if cap != 100 {
		t.Errorf("the ceiling for bash arrived as %d, want 100 — the stricter of the two she holds", cap)
	}
	// And a tool nobody capped arrives uncapped, which the gate reads as "no scope cap".
	if _, set := scope.MaxImpact["file_read"]; set {
		t.Error("a ceiling arrived for a tool nobody capped")
	}
}

// The name on the answer is the name the gate records in the audit line.
func TestTheNameOnTheAnswerIsWhatTheGateRecords(t *testing.T) {
	scope := asScope(permissions.Resolve(
		permissions.User{Username: "alice", MaxIntent: 200}, nil, nil,
	))

	if usernameOf(scope) != "alice" {
		t.Errorf("the gate would record %q", usernameOf(scope))
	}
	if usernameOf(nil) != "" {
		t.Errorf("a run with no principal records %q, want empty", usernameOf(nil))
	}
}
