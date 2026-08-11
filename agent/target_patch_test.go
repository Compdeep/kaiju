package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// applyRunTarget fills in a missing target on steps whose tool needs one and
// strips a stray target from steps whose tool does not — the fix that stops Holmes/microplanner
// grafts from failing "needs-target requires step.target".
func TestApplyRunTarget(t *testing.T) {
	reg := toolapi.NewRegistry()
	if err := reg.Register(&nodeStub{}); err != nil {
		t.Fatalf("register node: %v", err)
	}
	if err := reg.Register(&fleetStub{}); err != nil {
		t.Fatalf("register fleet: %v", err)
	}

	steps := []PlanStep{
		{Tool: "node_stub"},                     // needs-target, no target → default
		{Tool: "node_stub", Target: "explicit"}, // needs-target, has target → kept
		{Tool: "fleet_stub", Target: "stripme"}, // no-target, has target → stripped
		{Tool: "unknown_tool"},                  // not in registry → untouched
	}
	applyRunTarget(steps, "peer-9", reg)

	if steps[0].Target != "peer-9" {
		t.Errorf("needs-target, no target → %q, want peer-9", steps[0].Target)
	}
	if steps[1].Target != "explicit" {
		t.Errorf("needs-target explicit target overwritten → %q, want explicit", steps[1].Target)
	}
	if steps[2].Target != "" {
		t.Errorf("no-target target not stripped → %q, want empty", steps[2].Target)
	}
	if steps[3].Target != "" {
		t.Errorf("unknown tool should be left untouched → %q", steps[3].Target)
	}
}

// With no investigation target, a needs-target step must be LEFT untargeted so the
// dispatcher rejects it — it must never be silently stamped to run locally.
func TestPatchStepTargets_NoTarget(t *testing.T) {
	reg := toolapi.NewRegistry()
	if err := reg.Register(&nodeStub{}); err != nil {
		t.Fatalf("register node: %v", err)
	}
	steps := []PlanStep{{Tool: "node_stub"}}
	applyRunTarget(steps, "", reg)
	if steps[0].Target != "" {
		t.Errorf("needs-target with no investigation target → %q, want empty (rejected, not run locally)", steps[0].Target)
	}
}

// Two tools that differ only in whether they need to be told a machine, which
// is the whole of what the patching decides.

type nodeStub struct{}

func (*nodeStub) Name() string                { return "node_stub" }
func (*nodeStub) Description() string         { return "needs a machine" }
func (*nodeStub) RequiresTarget() bool        { return true }
func (*nodeStub) Impact(map[string]any) int   { return 0 }
func (*nodeStub) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (*nodeStub) Execute(_ context.Context, _ map[string]any) (string, error) {
	return "", nil
}

type fleetStub struct{}

func (*fleetStub) Name() string                { return "fleet_stub" }
func (*fleetStub) Description() string         { return "needs no machine" }
func (*fleetStub) RequiresTarget() bool        { return false }
func (*fleetStub) Impact(map[string]any) int   { return 0 }
func (*fleetStub) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (*fleetStub) Execute(_ context.Context, _ map[string]any) (string, error) {
	return "", nil
}
