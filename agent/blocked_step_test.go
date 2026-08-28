package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A dependency that failed with nothing to read blocks the step, and says so in
// a way the scheduler can act on rather than only print.
func TestADependencyThatFailedWithNothingToReadBlocksTheStep(t *testing.T) {
	g, dep := graphWithDep(t, "", StateFailed)
	n := &Node{Params: map[string]any{"x": "${node." + dep + ".output}"}}
	n.ID = g.AddNode(n)

	err := substituteTemplates(n, g, nil)
	var blocked *blockedByDep
	if !errors.As(err, &blocked) {
		t.Fatalf("error = %v (%T), want one the scheduler can recognise as a blocked step", err, err)
	}
	if blocked.DepID != dep || blocked.State != StateFailed {
		t.Errorf("blocked = %+v, want it to name %s and the state it was in", blocked, dep)
	}
	// The wording is what a person reads in the trace, and it has not changed.
	if !strings.Contains(err.Error(), "has empty result (failed)") {
		t.Errorf("error reads %q, want it to say the dependency had no result", err)
	}
}

// A dependency that RESOLVED and produced nothing is a different thing: the
// wiring named a step that yields no value, which is the plan being wrong. That
// stays a failure of the step that asked.
func TestADependencyThatResolvedEmptyIsStillAFailure(t *testing.T) {
	g, dep := graphWithDep(t, "", StateResolved)
	n := &Node{Params: map[string]any{"x": "${node." + dep + ".output}"}}
	n.ID = g.AddNode(n)

	err := substituteTemplates(n, g, nil)
	if err == nil {
		t.Fatal("a reference to a resolved step with no result was accepted")
	}
	var blocked *blockedByDep
	if errors.As(err, &blocked) {
		t.Errorf("error = %v, want a failure of this step rather than a blocked one", err)
	}
}

// failingTool reports an error and leaves nothing behind, which is what a step
// downstream of it then has to contend with.
type failingTool struct{ name string }

func (f *failingTool) Name() string                { return f.name }
func (f *failingTool) Description() string         { return "fails, for the end-to-end tests" }
func (f *failingTool) Impact(map[string]any) int   { return toolapi.ImpactObserve }
func (f *failingTool) RequiresTarget() bool        { return false }
func (f *failingTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f *failingTool) Execute(context.Context, map[string]any) (string, error) {
	return "", errors.New("the listing could not be read")
}

// One broken step is reported once.
//
// A step that never ran because the step before it failed used to be recorded
// as a failure of its own: a run with a single cause showed two failed nodes in
// the trace, and handed the aggregator two things to account for. It is marked
// skipped now, with the reason kept, and the run still fails where it actually
// broke.
func TestAStepWhoseDependencyFailedIsSkippedNotFailed(t *testing.T) {
	broken := &failingTool{name: "process_list"}
	reader := &countingTool{name: "get_process"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "operate"}},
		"plan": plan(
			step("process_list", "listing", nil),
			map[string]any{
				"tool": "get_process", "tag": "deliver", "depends_on": []int{0},
				"params": map[string]any{"filter": "${step.0.output}"},
			},
		),
		"reflector_decision": {Args: map[string]any{"decision": "conclude", "outcome": "the listing failed"}},
	})
	a := agentWithCompute(t, model, broken, reader)

	res, err := a.RunDAGSync(context.Background(), operateTrigger("what is running?"))
	if err != nil {
		t.Fatalf("the run failed: %v (stages called: %v)", err, model.functionsCalled())
	}
	nodes := traceNodes(t, res)

	if got := nodeWithTag(t, nodes, "listing")["state"]; got != "failed" {
		t.Errorf("the step that broke has state %v, want failed", got)
	}
	deliver := nodeWithTag(t, nodes, "deliver")
	if got := deliver["state"]; got != "skipped" {
		t.Errorf("the step that never ran has state %v, want skipped — it was not tried, so it did not fail", got)
	}
	if errText, _ := deliver["err"].(string); !strings.Contains(errText, "has empty result (failed)") {
		t.Errorf("the skipped step reads %q, want it to keep why it never ran", errText)
	}
	if reader.calls != 0 {
		t.Errorf("the skipped step ran %d times", reader.calls)
	}
}
