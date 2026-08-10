package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/gates"
)

// countingExecutor records whether a step was handed to another machine.
type countingExecutor struct{ called bool }

func (e *countingExecutor) Execute(context.Context, RemoteRequest) (string, error) {
	e.called = true
	return `{"type":"status","status":"ok"}`, nil
}

// Which machine a step runs on, and when that is a question at all.
//
// An application that supplied no executor runs everything where the agent
// runs. There is one answer, so a step that names no machine is not an omission
// and refusing it would refuse nearly every step — most tools declare that they
// want a target, and on a node that cannot dispatch, that declaration is about
// nothing.
//
// An application that CAN send work elsewhere is in the other position. "Here"
// is a choice among machines, so a step that names none is a step whose location
// nobody decided, and running it here is a guess.
//
// One rule, one predicate: whether an executor was supplied.

// fireTargetNode runs a node through the dispatcher and returns what came back.
func fireTargetNode(t *testing.T, a *Agent, n *Node) nodeCompletion {
	t.Helper()
	graph := NewGraph()
	n.ID = graph.AddNode(n)
	ch := make(chan nodeCompletion, 1)
	a.fireNode(context.Background(), n, graph, NewBudget(100, 10, 50, 50, 0),
		ch, "", newToolThrottle(), gates.Intent(2), nil)
	return <-ch
}

// targetContractAgent builds an agent whose registry holds one tool that wants
// a target and one that does not. Passing an executor makes it the kind of
// application that can send work to another machine; passing nil makes it the
// kind that cannot.
func targetContractAgent(t *testing.T, exec RemoteExecutor) *Agent {
	t.Helper()
	d := t.TempDir()
	a, err := New(Config{
		PathConfig:     PathConfig{Workspace: d, MetadataDir: d, DataDir: d},
		IdentityConfig: IdentityConfig{NodeID: "this-node"},
		Capabilities:   Capabilities{Remote: exec},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, tool := range []targetTool{{"inspect_host", true}, {"query_store", false}} {
		if err := a.registry.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

// A node that cannot dispatch runs an untargeted step rather than refusing it.
//
// This is the case that was broken where the rule lived in the application: 51
// of the 62 tools an endpoint registers declare that they want a target, and
// every one of their steps was rejected on a machine whose only possible answer
// was "here".
func TestWithNoExecutorAStepNeedsNoTarget(t *testing.T) {
	a := targetContractAgent(t, nil)

	got := fireTargetNode(t, a, &Node{Type: NodeTool, ToolName: "inspect_host", Tag: "look"})
	if got.Err != nil {
		t.Errorf("a step was refused for naming no machine on a node that has only "+
			"one: %v", got.Err)
	}
}

// With an executor, the same step is refused, and the message says what to
// write instead.
func TestWithAnExecutorAStepMustNameItsMachine(t *testing.T) {
	a := targetContractAgent(t, &countingExecutor{})

	got := fireTargetNode(t, a, &Node{Type: NodeTool, ToolName: "inspect_host", Tag: "look"})
	if got.Err == nil {
		t.Fatal("a step naming no machine ran somewhere without anyone choosing where")
	}
	if !strings.Contains(got.Err.Error(), "requires step.target") {
		t.Errorf("error = %q, which does not say what is missing", got.Err)
	}
	if !strings.Contains(got.Err.Error(), selfTarget) {
		t.Errorf("error = %q, which does not say how to ask for this machine", got.Err)
	}
}

// "self" is a spelling for this machine, resolved before anything decides where
// the step goes — so it runs here and is not handed to the executor.
func TestSelfMeansThisMachineAndStaysHere(t *testing.T) {
	exec := &countingExecutor{}
	a := targetContractAgent(t, exec)

	got := fireTargetNode(t, a, &Node{
		Type: NodeTool, ToolName: "inspect_host", Target: selfTarget, Tag: "look",
	})
	if got.Err != nil {
		t.Fatalf("self was not accepted as a target: %v", got.Err)
	}
	if exec.called {
		t.Error("a step targeting this machine was sent to another one")
	}
}

// A tool that takes no target does not carry one. The planner naming a machine
// for a step that has nothing to do with machines would otherwise send it there.
func TestATargetIsStrippedFromAToolThatTakesNone(t *testing.T) {
	exec := &countingExecutor{}
	a := targetContractAgent(t, exec)

	n := &Node{Type: NodeTool, ToolName: "query_store", Target: "other-machine", Tag: "read"}
	if got := fireTargetNode(t, a, n); got.Err != nil {
		t.Fatalf("fire: %v", got.Err)
	}
	if exec.called {
		t.Error("a tool that takes no target was dispatched to a machine anyway")
	}
	if n.Target != "" {
		t.Errorf("target = %q, want it stripped", n.Target)
	}
}

// Compute and the reflection types run where the agent runs, so the contract
// does not apply to them however the plan is written. Checked because the rule
// is written for tool nodes and a change to that condition would be silent.
func TestTheContractDoesNotApplyToComputeNodes(t *testing.T) {
	a := targetContractAgent(t, &countingExecutor{})

	got := fireTargetNode(t, a, &Node{Type: NodeCompute, ToolName: "compute", Tag: "build"})
	if got.Err != nil && strings.Contains(got.Err.Error(), "requires step.target") {
		t.Errorf("a compute node was held to the target contract: %v", got.Err)
	}
}
