package agent

import (
	"context"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// repairRankAgent holds the two things the rank guard reads: a registry
// carrying compute, and the intent registry that ranks it.
func repairRankAgent(t *testing.T) *Agent {
	t.Helper()
	intents, _ := newTestRegistry(t)
	reg := toolapi.NewRegistry()
	reg.Register(&mockTool{name: computeToolName, impact: toolapi.ImpactAffect})
	// A client that cannot connect rather than none at all. The dispatch starts
	// the microplanner in a goroutine, and with a nil client that goroutine dies
	// on a nil dereference — caught by guardNodeCompletion, so the test still
	// passes, but it prints a panic stack that reads as a broken test rather
	// than as the fixture it is. A refused connection fails the same way and
	// says so in one line.
	return &Agent{
		registry:       reg,
		intentRegistry: intents,
		llm:            llm.NewClient("http://127.0.0.1:1", "", "none"),
		executor:       llm.NewClient("http://127.0.0.1:1", "", "none"),
	}
}

// dispatchAt runs the real dispatcher at one rank and reports whether a
// microplanner node was grafted. Driven through dispatchMicroplannerWithRCA
// rather than a copy of its condition, so a change to the guard that a
// reimplementation would agree with still fails here.
func dispatchAt(t *testing.T, intent gates.Intent) (grafted bool, err error) {
	t.Helper()
	a := repairRankAgent(t)
	graph := NewGraph()
	parent := graph.AddNode(&Node{Type: NodeHolmes, Tag: "analyse_1_iter_1"})
	budget := NewBudget(50, 10, 50, 10, time.Minute)
	ch := make(chan nodeCompletion, 4)
	rca := &RCAReport{RootCause: "cron authenticates through PAM, which reads the shadow file"}

	before := graph.NodeCount()
	id, err := dispatchMicroplannerWithRCA(context.Background(), a, graph, budget, ch,
		Trigger{}, parent, 1, "cron read /etc/shadow and nothing explains it", rca, nil, intent)
	return id != "" || graph.NodeCount() > before, err
}

// A run at observe rank asked to be told what is wrong, not to have it changed.
// Dispatching anyway spends a microplanner to arrive at a gate refusal, which
// reaches the reflector as a failed step rather than as an answer.
func TestRepairIsSkippedBelowComputeRank(t *testing.T) {
	grafted, err := dispatchAt(t, gates.Intent(toolapi.ImpactObserve))
	if err != nil {
		t.Fatalf("dispatch errored instead of declining: %v", err)
	}
	if grafted {
		t.Fatal("a run at observe rank grafted a repair that edits files")
	}
}

// At the rank compute needs, the repair runs exactly as it did before this
// guard existed — it takes nothing from a run that could always have it.
func TestRepairRunsAtComputeRankAndAbove(t *testing.T) {
	for _, rank := range []int{toolapi.ImpactAffect, toolapi.ImpactControl} {
		grafted, err := dispatchAt(t, gates.Intent(rank))
		if err != nil {
			t.Fatalf("rank %d: dispatch errored: %v", rank, err)
		}
		if !grafted {
			t.Errorf("rank %d: a run at or above compute's rank was refused its repair", rank)
		}
	}
}

// Declining is not failing. The caller increments inflight on a returned id and
// logs a dispatch failure on an error, so a guard that reported itself as an
// error would show in the trace as a broken step rather than a decision.
func TestSkippedRepairIsNotAnError(t *testing.T) {
	_, err := dispatchAt(t, gates.Intent(toolapi.ImpactObserve))
	if err != nil {
		t.Fatalf("the guard returned an error; the run will log a dispatch failure: %v", err)
	}
}

// The diagnosis is not what is gated. Holmes must be reachable at every rank —
// that is the point of dropping debug to Observe: the one stage that forms and
// tests a hypothesis has to run on the investigations that cannot write.
func TestDiagnosisIsReachableAtEveryRank(t *testing.T) {
	d := NewDebugTool(nil)
	for _, rank := range []int{toolapi.ImpactObserve, toolapi.ImpactAffect, toolapi.ImpactControl} {
		if d.Impact(nil) > rank {
			t.Errorf("debug is out of reach at rank %d; Holmes cannot run on those investigations", rank)
		}
	}
}
