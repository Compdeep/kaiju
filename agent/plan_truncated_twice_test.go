package agent

import (
	"context"
	"encoding/json"
	"testing"
)

// A plan cut off at the cap is asked for again, shorter — that is
// TestATruncatedPlanIsAskedForAgainShorter. The SECOND cut has no branch, and
// this is what the run does instead.
//
// The retry's finish_reason is still "length", so the `== "tool_calls"` gate
// below the re-ask is skipped and execution falls to the prose fallback. There
// the fragment is already gone: asToolReply moved it into ToolCalls and emptied
// Content, so `raw` is "" and the executive reports a conversational answer with
// nothing in it. The scheduler reads that empty text as "no plan, no reply" and
// answers from the chat lane.
//
// Two things follow, and neither is a failure the caller can see:
//
//   - an application supplying Answer gets SyncResult.Data nil, because the
//     chat-lane return never calls writeAnswer. An application that casts Data
//     back to its own type has nothing to cast, and reports the run as having
//     produced no result.
//   - a chat caller gets a confident answer written with no evidence at all —
//     no tool ran, and nothing in the reply says so.
//
// Asserted as a description of today's behaviour. When the second cut is
// handled, this test fails and says which half changed.
func TestAPlanTruncatedTwiceAnswersFromTheChatLaneInstead(t *testing.T) {
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

	// The retry happened, and it bought nothing.
	if n := model.callsTo("plan"); n != 2 {
		t.Errorf("the planner was called %d times, want 2 — the cut plan and the shorter one", n)
	}
	if tool.calls != 0 {
		t.Errorf("a step ran off a plan that never parsed (%d calls)", tool.calls)
	}

	// An answer with no evidence behind it. This is the half a chat caller sees.
	if res.Outcome == "" {
		t.Error("outcome is empty; the chat fallback is expected to answer, which is the defect this records")
	}

	// No Data, because the chat-lane return never reaches writeAnswer. This is
	// the half an application supplying Answer sees: it casts Data back to its
	// own type, finds nothing, and reports a run that produced no result.
	if res.Data != nil {
		t.Errorf("Data = %v; today's chat-lane return carries none, so a change here is the fix landing", res.Data)
	}
}
