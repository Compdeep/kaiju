package agent

import (
	"context"
	"encoding/json"
	"errors"
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

// It is an ending, not a failure.
//
// The planner having no move and the reflector wanting more is two stages
// disagreeing, and the run stops. Reported as a failed step it showed a broken
// row in the trace for something that simply finished — and "re-plan returned no
// steps: the reflector decided work remains, and stopping is its decision, not
// the planner's" describes which component owns a decision rather than what
// happened.
func TestAnEmptyRePlanEndsTheRunWithoutFailingIt(t *testing.T) {
	err := error(&ExecutiveNoMoveError{Answer: "recalled text"})

	var noMove *ExecutiveNoMoveError
	if !errors.As(err, &noMove) {
		t.Fatal("the outcome is not distinguishable from an ordinary error, so the " +
			"scheduler cannot tell a stop from a failure")
	}
	if got := err.Error(); !strings.Contains(got, "no further step") {
		t.Errorf("the message does not say what happened: %q", got)
	}
	if strings.Contains(err.Error(), "stopping is its decision") {
		t.Error("the message still argues about which component owns the decision " +
			"instead of describing the situation")
	}
}

// The scheduler resolves that node rather than failing it, and does NOT take the
// planner's answer as the run's outcome.
func TestTheSchedulerTreatsNoMoveAsAStop(t *testing.T) {
	src := readSource(t, "scheduler.go")
	i := strings.Index(src, "ExecutiveNoMoveError")
	if i < 0 {
		t.Fatal("the scheduler does not recognise the outcome, so it falls through " +
			"to the failure branch and shows a broken step")
	}
	branch := src[i:min(i+900, len(src))]
	if !strings.Contains(branch, `State: "resolved"`) {
		t.Error("the planning node is still marked failed for a run that simply stopped")
	}
	if strings.Contains(branch, "noMove.Answer)") {
		t.Error("the planner's recalled answer is being used as the run's outcome — " +
			"see TestAnEmptyRePlanIsRefused")
	}
}
