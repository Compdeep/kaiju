package agent

import (
	"context"
	"encoding/json"
	"testing"
)

// A plan cut off at the cap is asked for again, shorter — that is
// TestATruncatedPlanIsAskedForAgainShorter. When the shorter one is cut too,
// the steps that finished before the cut are run rather than thrown away.
//
// They used to be thrown away. A cut reply says "length" and not "tool_calls":
// asToolReply leaves the reason alone so a stage asking for a shorter plan can
// still see it was cut, and moves the arguments into the call regardless. The
// executive gated its parse on the reason alone, so a reply carrying a plan was
// refused and execution fell to the prose branch — which reads Content, emptied
// by that same move. The run then answered from the chat lane: an application
// supplying Answer got SyncResult.Data nil and reported no result, and a chat
// caller got a confident answer with no tool behind it.
//
// One live run showed what that costs. The planner looped, was cut at 8,192
// tokens, and the reply held twenty-four real steps before the repetition
// began. All twenty-four were discarded and the run produced nothing.
func TestAPlanTruncatedTwiceRunsTheStepsThatFinished(t *testing.T) {
	tool := &countingTool{name: "process_list"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{
			"mode": "agent", "intent": "observe", "skills": []string{},
		}},
		"reflector_decision": {Args: map[string]any{
			"decision": "conclude", "summary": "two processes", "outcome": "two processes are running",
		}},
	})
	// Both the plan and the shorter re-ask run into the cap.
	cut := stubReply{Args: map[string]any{"steps": []map[string]any{step("process_list", "procs", nil)}}, Cut: true}
	model.answerNth("plan", cut, cut)

	a := agentOnStub(t, model, tool)
	res, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"what is running?"}`),
	})
	if err != nil {
		t.Fatalf("the run errored rather than falling through: %v", err)
	}
	if res == nil {
		t.Fatal("the run returned no result at all")
	}

	// The shorter re-ask still happens: the first reply was cut with nothing to
	// salvage, which is what that branch is for.
	if n := model.callsTo("plan"); n != 2 {
		t.Errorf("the planner was called %d times, want 2 — the cut plan and the shorter one", n)
	}

	// And the second cut is no longer wasted. The step that finished before the
	// cut is whole, was already paid for, and runs.
	if tool.calls == 0 {
		t.Error("no step ran: the steps that finished before the cut were thrown away again")
	}

	// An answer with a tool behind it, rather than the chat lane inventing one.
	if res.Outcome == "" {
		t.Error("the run produced no outcome")
	}
}
