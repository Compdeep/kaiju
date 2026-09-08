package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// theCutPlan is a real reply, captured from a run that failed: the planner was
// asked for a plan over a strict schema, fell into a repetition loop, and the
// provider cut it at the cap mid-string. 21,137 characters, 66 complete steps
// before the cut.
func theCutPlan(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/truncated_plan_length.json")
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	return string(b)
}

// The reply as the executive receives it.
//
// The planner sends a strict schema, so the provider answers with content and
// asToolReply moves it into the tool call. What that leaves is asserted against
// the real function in llm.TestAsToolReply_ACutReplyKeepsSayingLength; this
// builds the same shape so the agent-side half can be tested without exporting
// it.
func theCutReplyAsReceived(t *testing.T) llm.Choice {
	t.Helper()
	return llm.Choice{
		Message: llm.Message{
			Content: "", // blanked when the arguments were moved
			ToolCalls: []llm.ToolCall{{
				ID:       "call_plan",
				Type:     "function",
				Function: llm.FunctionCall{Name: "plan", Arguments: theCutPlan(t)},
			}},
		},
		FinishReason: "length", // left alone on purpose
	}
}

// The steps are there to be had. salvageTruncatedPlan exists for exactly this
// reply and recovers every step that closed before the cut.
func TestACutPlanStillCarriesItsFinishedSteps(t *testing.T) {
	choice := theCutReplyAsReceived(t)

	salvaged := salvageTruncatedPlan(stripFence(planArguments(choice)))
	if salvaged == "" {
		t.Fatal("nothing salvageable — the rest of this file assumes there is")
	}
	var payload executiveCallPayload
	if err := parseExecutivePayload(fixComputeStepParams(salvaged), &payload); err != nil {
		t.Fatalf("the salvaged plan does not parse: %v", err)
	}
	if len(payload.Steps) == 0 {
		t.Fatal("the salvaged plan has no steps")
	}
	t.Logf("recoverable steps: %d", len(payload.Steps))
}

// What the fix has to produce: a cut reply that carries arguments is parsed,
// the salvage inside the parser recovers the steps that finished, and the plan
// that comes out is the useful part of what the model wrote.
//
// The 24 steps before the model began repeating are a whole investigation —
// identify the process, its parent, its connections, then characterise the
// binary. MaxNodes truncates the rest. Losing them costs a run and a retry.
func TestACutPlanRecoversTheStepsThatFinished(t *testing.T) {
	choice := theCutReplyAsReceived(t)

	var payload executiveCallPayload
	if err := parseExecutivePayload(fixComputeStepParams(planArguments(choice)), &payload); err != nil {
		t.Fatalf("the parser could not recover the plan: %v", err)
	}
	if len(payload.Steps) < 24 {
		t.Fatalf("recovered %d steps, want at least the 24 that were real", len(payload.Steps))
	}

	// The investigation is there, in order.
	// The first steps name real tools and are in the order the model wrote them.
	for i := 0; i < 5; i++ {
		if payload.Steps[i].Tool == "" {
			t.Errorf("step %d has no tool", i)
		}
	}
	if payload.Steps[0].Tool != "get_process" || payload.Steps[1].Tool != "inspect_process" {
		t.Errorf("the plan does not start where the model started it: %q, %q",
			payload.Steps[0].Tool, payload.Steps[1].Tool)
	}
	t.Logf("recovered %d steps; first five: %s %s %s %s %s",
		len(payload.Steps),
		payload.Steps[0].Tool, payload.Steps[1].Tool, payload.Steps[2].Tool,
		payload.Steps[3].Tool, payload.Steps[4].Tool)
}

// The whole path, on a reply that really came back cut.
//
// The stub answers plan() with the captured 21KB fragment and reports
// finish_reason "length", which is what the endpoint said. The steps that
// finished before the cut have to reach the graph and run.
//
// Before the gate admitted a cut reply this asserted zero calls: the fragment
// was refused, execution fell to the prose branch, and a run that had a
// twenty-four step plan in hand produced nothing.
func TestARealCutPlanRunsItsFinishedSteps(t *testing.T) {
	// The tools the captured plan names, so its steps have somewhere to land.
	procs := &countingTool{name: "get_process"}
	inspect := &countingTool{name: "inspect_process"}
	conns := &countingTool{name: "get_connections"}
	sh := &countingTool{name: "bash"}

	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{
			"mode": "agent", "intent": "observe", "skills": []string{},
		}},
		"reflector_decision": {Args: map[string]any{
			"decision": "conclude", "summary": "looked at the process", "outcome": "benign",
		}},
	})
	model.answerNth("plan", stubReply{RawArgs: theCutPlan(t), Cut: true})

	a := agentOnStub(t, model, procs, inspect, conns, sh)
	res, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"what is this process doing?"}`),
	})
	if err != nil {
		t.Fatalf("the run errored: %v", err)
	}
	if res == nil {
		t.Fatal("the run returned no result")
	}

	ran := procs.calls + inspect.calls + conns.calls + sh.calls
	if ran == 0 {
		t.Fatal("no step ran — the finished steps of a cut plan were thrown away")
	}
	// The plan opens with these, and they are what makes it worth recovering.
	if procs.calls == 0 {
		t.Error("get_process never ran, so the recovered plan is not the one the model wrote")
	}
	if inspect.calls == 0 {
		t.Error("inspect_process never ran")
	}
	t.Logf("steps run: get_process=%d inspect_process=%d get_connections=%d bash=%d (total %d)",
		procs.calls, inspect.calls, conns.calls, sh.calls, ran)
}
