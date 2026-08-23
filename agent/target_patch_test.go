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
	if err := reg.Register(&untargetedStub{}); err != nil {
		t.Fatalf("register the untargeted tool: %v", err)
	}

	steps := []PlanStep{
		{Tool: "node_stub"},                          // needs-target, no target → default
		{Tool: "node_stub", Target: "explicit"},      // needs-target, has target → kept
		{Tool: "untargeted_stub", Target: "stripme"}, // no-target, has target → stripped
		{Tool: "unknown_tool"},                       // not in registry → untouched
	}
	applyRunTarget(steps, "host-9", reg)

	if steps[0].Target != "host-9" {
		t.Errorf("needs-target, no target → %q, want host-9", steps[0].Target)
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

type untargetedStub struct{}

func (*untargetedStub) Name() string                { return "untargeted_stub" }
func (*untargetedStub) Description() string         { return "needs no machine" }
func (*untargetedStub) RequiresTarget() bool        { return false }
func (*untargetedStub) Impact(map[string]any) int   { return 0 }
func (*untargetedStub) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (*untargetedStub) Execute(_ context.Context, _ map[string]any) (string, error) {
	return "", nil
}
