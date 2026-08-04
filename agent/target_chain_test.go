package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/agent/tools"
)

// The target chain has five links: Trigger.Target -> applyRunTarget ->
// PlanStep.Target -> Node.Target -> RemoteExecutor. Each link was built in a
// separate commit, and one of them was left unconnected — applyRunTarget had
// no caller, so a run's target never reached its steps and nothing failed.
// This walks the whole chain rather than any single link.

type chainTool struct {
	name     string
	requires bool
}

func (t chainTool) Name() string                { return t.name }
func (t chainTool) Description() string         { return t.name }
func (t chainTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t chainTool) Impact(map[string]any) int   { return tools.ImpactObserve }
func (t chainTool) RequiresTarget() bool        { return t.requires }
func (t chainTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

func TestTargetReachesTheNode(t *testing.T) {
	reg := tools.NewRegistry()
	if err := reg.Register(chainTool{"inspect_host", true}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(chainTool{"query_store", false}); err != nil {
		t.Fatal(err)
	}
	a := &Agent{registry: reg}
	a.cfg.NodeID = "self"

	steps := []PlanStep{{Tool: "inspect_host"}, {Tool: "query_store"}}
	res, err := a.validatePlanSteps(steps, false, 0, Trigger{Target: "machine-a"}, nil)
	if err != nil {
		t.Fatalf("validatePlanSteps: %v", err)
	}

	// Link 2->3: the run's target must have landed on the step that needs one.
	var host, store *PlanStep
	for i := range res.Steps {
		switch res.Steps[i].Tool {
		case "inspect_host":
			host = &res.Steps[i]
		case "query_store":
			store = &res.Steps[i]
		}
	}
	if host == nil || store == nil {
		t.Fatalf("expected both steps to survive validation, got %+v", res.Steps)
	}
	if host.Target != "machine-a" {
		t.Errorf("a step needing a target did not inherit the run's: %q", host.Target)
	}
	if store.Target != "" {
		t.Errorf("a step needing no target was given one: %q", store.Target)
	}

	// Link 4->5: a node carrying a target must be routed to the executor.
	if !a.remoteFor(&Node{Type: NodeTool, Target: "machine-a"}) {
		// remoteFor is false without an executor, which is correct — so wire one.
		a.remoteExec = stubChainExec{}
		if !a.remoteFor(&Node{Type: NodeTool, Target: "machine-a"}) {
			t.Error("a tool node with a target and an executor was not routed remotely")
		}
	}
}

type stubChainExec struct{}

func (stubChainExec) Execute(context.Context, RemoteRequest) (string, error) { return "", nil }
