package agent

import (
	"testing"
	"time"
)

// Two minutes is the floor, and it is measured rather than chosen.
//
// gpt-5 is the slowest model anybody would ordinarily plan with: 77.3 seconds
// median over three samples on the real planner prompt. A deadline under two
// minutes cuts off the baseline while it is working, and each cut costs a whole
// second call to recover — more than the deadline saved.
func TestRoundBudget_NeverBelowTheFloor(t *testing.T) {
	a := &Agent{}
	for _, effort := range []string{EffortMinimal, EffortLow, EffortDefault, EffortMedium, ""} {
		if got := a.roundBudget(Trigger{ReasoningEffort: effort}); got < minRoundBudget {
			t.Errorf("effort %q gives %s, below the %s floor", effort, got, minRoundBudget)
		}
	}
	// And the floor fits the baseline with room to spare.
	if minRoundBudget < 78*time.Second {
		t.Errorf("the floor is %s; gpt-5 takes 77.3s on the planner prompt", minRoundBudget)
	}
}

// Asking a model to think LESS is a different thing from giving the call less
// time, so the weakest efforts land on the floor rather than below it.
func TestRoundBudget_TheWeakEffortsLandOnTheFloor(t *testing.T) {
	a := &Agent{}
	for _, effort := range []string{EffortMinimal, EffortLow} {
		if got := a.roundBudget(Trigger{ReasoningEffort: effort}); got != minRoundBudget {
			t.Errorf("effort %q gives %s, want the %s floor", effort, got, minRoundBudget)
		}
	}
}

// A stronger effort buys more time, which is the point of the dial.
func TestRoundBudget_StrongerEffortsBuyMoreTime(t *testing.T) {
	a := &Agent{}
	prev := time.Duration(0)
	for _, effort := range []string{EffortLow, EffortDefault, EffortHigh, EffortXHigh, EffortMax} {
		got := a.roundBudget(Trigger{ReasoningEffort: effort})
		if got < prev {
			t.Errorf("effort %q gives %s, less than the effort below it (%s)", effort, got, prev)
		}
		prev = got
	}
	if a.roundBudget(Trigger{ReasoningEffort: EffortMax}) <= a.roundBudget(Trigger{ReasoningEffort: EffortDefault}) {
		t.Error("max buys no more time than the default")
	}
}

// The run's own effort beats the node's setting — the precedence reasoningFor
// applies at the call seam.
func TestRoundBudget_TheRunsEffortWins(t *testing.T) {
	a := &Agent{}
	a.cfg.LLMReasoningEffort = EffortHigh
	if got := a.roundBudget(Trigger{}); got != effortBudget[EffortHigh] {
		t.Errorf("with no run effort = %s, want the node's high", got)
	}
	if got := a.roundBudget(Trigger{ReasoningEffort: EffortXHigh}); got != effortBudget[EffortXHigh] {
		t.Errorf("with a run effort = %s, want the run's xhigh", got)
	}
}

// An effort nobody recognises gets the default, not zero. A deadline of zero is
// a context that has already expired.
func TestRoundBudget_AnUnknownEffortGetsTheDefault(t *testing.T) {
	a := &Agent{}
	if got := a.roundBudget(Trigger{ReasoningEffort: "enthusiastic"}); got != effortBudget[EffortDefault] {
		t.Errorf("an unknown effort gives %s, want the default", got)
	}
}
