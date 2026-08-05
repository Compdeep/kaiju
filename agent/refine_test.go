package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/gates"
)

// Preflight refinement.
//
// Two capabilities in one hook: correct what preflight concluded using facts
// this package cannot have, or reply with a question when the request cannot be
// acted on as written.
//
// The second is the one Kaiju was missing. Today only the PLANNER can ask, by
// emitting a gap, which happens after tools and skills are loaded and only if
// the model chooses to ask rather than guess.

func TestRefineNilLeavesPreflightAlone(t *testing.T) {
	pf := &PreflightResult{Mode: "agent", Intent: gates.Intent(1)}
	got, reply := (&Agent{}).refinePreflight(context.Background(), pf, &Trigger{})

	if got != pf {
		t.Error("a nil refinement changed the result")
	}
	if reply != "" {
		t.Errorf("reply = %q, want none", reply)
	}
}

func TestRefineCorrectsTheResult(t *testing.T) {
	a := &Agent{refine: func(_ context.Context, pf *PreflightResult, _ *Trigger) (*PreflightResult, string, error) {
		out := *pf
		out.Skills = []string{"fleet"}
		return &out, "", nil
	}}

	got, reply := a.refinePreflight(context.Background(), &PreflightResult{Mode: "agent"}, &Trigger{})
	if reply != "" {
		t.Fatalf("reply = %q, want none", reply)
	}
	if len(got.Skills) != 1 || got.Skills[0] != "fleet" {
		t.Errorf("skills = %v, want the refinement's", got.Skills)
	}
}

// TestRefineCanAskInsteadOfPlanning is the capability being added.
func TestRefineCanAskInsteadOfPlanning(t *testing.T) {
	const question = "Which machine do you mean — web-1 or web-2?"
	a := &Agent{refine: func(context.Context, *PreflightResult, *Trigger) (*PreflightResult, string, error) {
		return nil, question, nil
	}}

	_, reply := a.refinePreflight(context.Background(), &PreflightResult{Mode: "agent"}, &Trigger{})
	if reply != question {
		t.Errorf("reply = %q, want the question verbatim", reply)
	}
}

// TestRefineFailureDoesNotStopTheRun: a refinement that cannot run must not
// block a run that would otherwise have proceeded. It is an improvement on
// preflight's answer, not a precondition for having one.
func TestRefineFailureDoesNotStopTheRun(t *testing.T) {
	pf := &PreflightResult{Mode: "agent"}
	a := &Agent{refine: func(context.Context, *PreflightResult, *Trigger) (*PreflightResult, string, error) {
		return nil, "", errors.New("the fleet store is unreachable")
	}}

	got, reply := a.refinePreflight(context.Background(), pf, &Trigger{})
	if reply != "" {
		t.Errorf("a failed refinement produced a reply (%q) — an error is not a question", reply)
	}
	if got != pf {
		t.Error("a failed refinement changed the result; preflight's own answer should stand")
	}
}

// TestRefineRunsBeforeThePlanner pins the CALL SITE and its position. Asking
// after planning would defeat the point — the plan is the expensive part, and a
// model handed a task-shaped request guesses rather than asks.
func TestRefineRunsBeforeThePlanner(t *testing.T) {
	body := funcBody(t, readSource(t, "scheduler.go"), "runPlanAndSchedule")

	refineAt := strings.Index(body, "a.refinePreflight(ctx, graph.Preflight, &trigger)")
	plannerAt := strings.Index(body, "a.runExecutive(ctx, trigger, graph)")

	if refineAt < 0 {
		t.Fatal("the scheduler no longer consults the refinement")
	}
	if plannerAt < 0 {
		t.Fatal("the planner call was not found; this test needs updating")
	}
	if refineAt > plannerAt {
		t.Error("refinement runs after the planner — a question then costs a full plan, " +
			"which is the cost it exists to avoid")
	}
	if !strings.Contains(body, "ExecutiveConversationalError{Text: reply}") {
		t.Error("a reply is not returned as a conversational result, so callers would " +
			"see a question as a failure")
	}
}

// A refinement is host code running inside a run. It gets a deadline and its
// panics are contained — the plugin engine this replaced did both, and losing
// them meant a slow store could stall every run and a bad one could take the
// process down.

func TestRefinePanicKeepsPreflightsAnswer(t *testing.T) {
	pf := &PreflightResult{Mode: "agent"}
	a := &Agent{refine: func(context.Context, *PreflightResult, *Trigger) (*PreflightResult, string, error) {
		panic("the fleet store exploded")
	}}

	got, reply := a.refinePreflight(context.Background(), pf, &Trigger{})
	if got != pf {
		t.Error("a panicking refinement did not leave preflight's answer standing")
	}
	if reply != "" {
		t.Errorf("reply = %q, want none", reply)
	}
}

func TestRefineIsGivenADeadline(t *testing.T) {
	var hadDeadline bool
	a := &Agent{refine: func(ctx context.Context, pf *PreflightResult, _ *Trigger) (*PreflightResult, string, error) {
		_, hadDeadline = ctx.Deadline()
		return nil, "", nil
	}}

	a.refinePreflight(context.Background(), &PreflightResult{}, &Trigger{})
	if !hadDeadline {
		t.Error("the refinement ran with no deadline; a wedged one would stall the run")
	}
}

// A refinement that ignores its deadline still cannot hold the run: the context
// is cancelled and a well-behaved one returns. This pins that the bound exists
// and is short enough to matter.
func TestRefineDeadlineIsShort(t *testing.T) {
	if refineTimeout > 5*time.Second {
		t.Errorf("refineTimeout = %v, too long to be a bound worth having", refineTimeout)
	}
	if refineTimeout <= 0 {
		t.Error("refineTimeout must be positive or there is no bound at all")
	}
}
