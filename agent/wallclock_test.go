package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The wall clock bounds the work and never the answer.
//
// Two faults met here and produced the same symptom — a person asked a question
// and got nothing back. WallClock was carried on the Budget and read by nobody,
// so the setting had no effect; and the one place that noticed a finished
// context tested the execution context, so a run that ran out of time returned
// an error instead of the evidence it had already gathered.
//
// Driven end to end rather than as a unit, because the fault was in how four
// stages were wired to one context, and each of them worked.

// slowTool takes longer than the run is allowed, and stops when told to.
type slowTool struct {
	name    string
	started chan struct{}
}

func (s *slowTool) Name() string                { return s.name }
func (s *slowTool) Description() string         { return "takes longer than the run is allowed" }
func (s *slowTool) Impact(map[string]any) int   { return toolapi.ImpactObserve }
func (s *slowTool) RequiresTarget() bool        { return false }
func (s *slowTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s *slowTool) Execute(ctx context.Context, _ map[string]any) (string, error) {
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-time.After(30 * time.Second):
		return toolapi.ToolOK("listing", "finished after all", nil).JSON(), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// A run whose clock runs out still writes an answer from what it gathered.
func TestAnExpiredWallClockStillAnswers(t *testing.T) {
	tool := &slowTool{name: "process_list", started: make(chan struct{}, 1)}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{
			"mode": "agent", "intent": "observe", "skills": []string{},
		}},
		"plan": plan(step("process_list", "procs", nil)),
	})
	a := agentOnStubWith(t, model, DAGConfig{
		DAGEnabled: true, MaxNodes: 20, MaxLLMCalls: 20,
		DAGWallClock: 400 * time.Millisecond,
	}, tool)

	done := make(chan struct{})
	var res *SyncResult
	var err error
	go func() {
		defer close(done)
		res, err = a.RunDAGSync(context.Background(), Trigger{
			Type: "chat_query", Data: json.RawMessage(`{"query":"what is running?"}`),
		})
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the run never returned: the clock expired and the scheduler kept going")
	}

	if err != nil {
		t.Fatalf("an expired clock failed the run instead of answering it: %v (stages called: %v)",
			err, model.functionsCalled())
	}
	if res == nil || res.Outcome == "" {
		t.Fatalf("the run came back with no answer at all. Stages called: %v", model.functionsCalled())
	}
}

// The caller going away is not the clock expiring, and does not earn an answer:
// there is nobody left to read one, and writing it costs a model call.
func TestACallerThatGoesAwayGetsNoAnswer(t *testing.T) {
	tool := &slowTool{name: "process_list", started: make(chan struct{}, 1)}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{
			"mode": "agent", "intent": "observe", "skills": []string{},
		}},
		"plan": plan(step("process_list", "procs", nil)),
	})
	// No wall clock: the only thing that can stop this run is the caller.
	a := agentOnStubWith(t, model, DAGConfig{
		DAGEnabled: true, MaxNodes: 20, MaxLLMCalls: 20,
	}, tool)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var res *SyncResult
	var err error
	go func() {
		defer close(done)
		res, err = a.RunDAGSync(ctx, Trigger{
			Type: "chat_query", Data: json.RawMessage(`{"query":"what is running?"}`),
		})
	}()

	select {
	case <-tool.started:
	case <-time.After(20 * time.Second):
		cancel()
		t.Fatal("the planned tool never ran, so the run was not stopped mid-flight")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the run never returned after its caller went away")
	}

	if err == nil {
		t.Errorf("a run nobody is waiting on reported success: %+v", res)
	}
}
