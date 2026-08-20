package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Stopping is the reflector's decision, not the planner's.
//
// The reflector has three outcomes — continue, replan, conclude — and conclude
// is how a run stops. A re-plan happens only because it chose replan, which is
// it ruling that work remains. So a planner that answers instead of planning is
// overturning a decision that is not its own, and the run ends having done
// nothing it was asked to do while presenting a recalled answer as the result.
func TestAnEmptyRePlanIsRefused(t *testing.T) {
	const recalled = "RECALLED FROM MEMORY, NOT COMPUTED"

	tool := &countingTool{name: "process_list"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{
			"mode": "agent", "intent": "observe", "skills": []string{},
		}},
		"plan": plan(step("process_list", "procs", nil)),
	})

	// The reflector asks for more work; the planner then offers an answer and no
	// steps, which is the case under test.
	model.answerNth("submit_decision", stubReply{Args: map[string]any{
		"decision": "replan",
		"summary":  "nothing has actually been retrieved yet",
		"next":     "fetch the source and read it",
	}})
	model.answerNth("plan",
		plan(step("process_list", "procs", nil)),
		stubReply{Args: map[string]any{"answer": recalled, "steps": []any{}}},
	)

	a := agentOnStub(t, model, tool)
	res, err := a.RunDAGSync(context.Background(), Trigger{
		Type: "chat_query", Data: json.RawMessage(`{"query":"work it out"}`),
	})
	if err != nil {
		t.Fatalf("the run failed outright: %v (stages: %v)", err, model.functionsCalled())
	}
	if res == nil {
		t.Fatal("no result")
	}
	if strings.Contains(res.Outcome, recalled) {
		t.Errorf("the planner's recalled answer became the run's outcome, so it overturned the reflector:\n%s", res.Outcome)
	}
}
