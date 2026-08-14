package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A step that names a machine this agent cannot reach.
//
// It used to run here. The reasoning was that a target with no executor behaves
// as it did before targets existed — which held until a planner could name a
// machine, at which point the step does its work on the wrong one and says
// nothing. `process_kill` is the case that makes it concrete.
//
// These drive fireNode, which is where the decision is, rather than
// executeToolNode below it.

// The package already has a countingTool; this one exists to be refused, so it
// only has to say whether it ran.
type refusalTool struct{ runs int }

func (r *refusalTool) Name() string              { return "counter" }
func (r *refusalTool) Description() string       { return "records that it ran" }
func (r *refusalTool) Impact(map[string]any) int { return toolapi.ImpactObserve }
func (r *refusalTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (r *refusalTool) Execute(ctx context.Context, p map[string]any) (string, error) {
	return toolapi.StringResult(r.ExecuteTyped(ctx, p))
}
func (r *refusalTool) ExecuteTyped(context.Context, map[string]any) (toolapi.ToolMessage, error) {
	r.runs++
	return toolapi.ToolOK("counter", "ran", nil), nil
}

// fireOne runs one node through the dispatcher and returns its completion.
func fireOne(t *testing.T, a *Agent, n *Node) nodeCompletion {
	t.Helper()
	graph := NewGraph()
	id := graph.AddNode(n)
	ch := make(chan nodeCompletion, 1)
	a.fireNode(context.Background(), graph.Get(id), graph,
		NewBudget(20, 5, 20, 5, time.Minute), ch, "", newToolThrottle(), gates.Intent(0), nil)
	select {
	case c := <-ch:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("fireNode produced no completion")
		return nodeCompletion{}
	}
}

func agentWith(t *testing.T, tool toolapi.Tool) *Agent {
	t.Helper()
	reg, gate, _ := newTestStack(t)
	registry := toolapi.NewRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
	}
	a := &Agent{registry: registry, gate: gate, intentRegistry: reg}
	a.cfg.NodeID = "self"
	return a
}

func TestAStepForAnotherMachineWithNoExecutorIsRefused(t *testing.T) {
	tool := &refusalTool{}
	a := agentWith(t, tool)

	got := fireOne(t, a, &Node{Type: NodeTool, ToolName: "counter", Target: "machine-b"})

	if got.Err == nil {
		t.Fatal("the step was not refused — it ran on this machine instead of the one it named")
	}
	if tool.runs != 0 {
		t.Errorf("the tool ran %d times; a step for another machine must not run here", tool.runs)
	}
	if !strings.Contains(got.Err.Error(), "machine-b") {
		t.Errorf("err = %v; the refusal does not name the machine", got.Err)
	}
}

// "Here" is not the case above. A target equal to this agent's own id means the
// planner said to run it here, and it runs.
func TestAStepTargetedAtThisMachineStillRuns(t *testing.T) {
	tool := &refusalTool{}
	a := agentWith(t, tool)

	got := fireOne(t, a, &Node{Type: NodeTool, ToolName: "counter", Target: "self"})

	if got.Err != nil {
		t.Fatalf("a step targeted at this machine was refused: %v", got.Err)
	}
	if tool.runs != 1 {
		t.Errorf("the tool ran %d times, want 1", tool.runs)
	}
}

// And a step with no target at all is untouched by any of this.
func TestAStepWithNoTargetIsUnaffected(t *testing.T) {
	tool := &refusalTool{}
	a := agentWith(t, tool)

	if got := fireOne(t, a, &Node{Type: NodeTool, ToolName: "counter"}); got.Err != nil {
		t.Fatalf("a step with no target was refused: %v", got.Err)
	}
	if tool.runs != 1 {
		t.Errorf("the tool ran %d times, want 1", tool.runs)
	}
}

// failingExec is an executor that cannot reach anything, and whose error says
// nothing about which machine — which is what a transport error looks like.
type failingExec struct{}

func (failingExec) Execute(context.Context, RemoteRequest) (string, error) {
	return "", errors.New("dial tcp: i/o timeout")
}

// The step's whole point was the machine, so its failure names it. A transport
// error on its own does not.
func TestAFailedRemoteStepNamesTheMachine(t *testing.T) {
	a := agentWith(t, &refusalTool{})
	a.remoteExec = failingExec{}

	got := fireOne(t, a, &Node{Type: NodeTool, ToolName: "counter", Target: "machine-b"})

	if got.Err == nil {
		t.Fatal("a remote step that could not be reached reported success")
	}
	if !strings.Contains(got.Err.Error(), "machine-b") {
		t.Errorf("err = %v; the machine is not named", got.Err)
	}
	if !strings.Contains(got.Err.Error(), "i/o timeout") {
		t.Errorf("err = %v; the executor's own reason was dropped", got.Err)
	}
}
