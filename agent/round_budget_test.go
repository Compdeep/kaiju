package agent

import (
	"context"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
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
		if got := a.roundBudget(context.Background(), Heavy, Trigger{ReasoningEffort: effort}); got < minRoundBudget {
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
		if got := a.roundBudget(context.Background(), Heavy, Trigger{ReasoningEffort: effort}); got != minRoundBudget {
			t.Errorf("effort %q gives %s, want the %s floor", effort, got, minRoundBudget)
		}
	}
}

// A stronger effort buys more time, which is the point of the dial.
func TestRoundBudget_StrongerEffortsBuyMoreTime(t *testing.T) {
	a := &Agent{}
	prev := time.Duration(0)
	for _, effort := range []string{EffortLow, EffortDefault, EffortHigh, EffortXHigh, EffortMax} {
		got := a.roundBudget(context.Background(), Heavy, Trigger{ReasoningEffort: effort})
		if got < prev {
			t.Errorf("effort %q gives %s, less than the effort below it (%s)", effort, got, prev)
		}
		prev = got
	}
	if a.roundBudget(context.Background(), Heavy, Trigger{ReasoningEffort: EffortMax}) <= a.roundBudget(context.Background(), Heavy, Trigger{ReasoningEffort: EffortDefault}) {
		t.Error("max buys no more time than the default")
	}
}

// The run's own effort beats the node's setting — the precedence reasoningFor
// applies at the call seam.
func TestRoundBudget_TheRunsEffortWins(t *testing.T) {
	a := &Agent{}
	a.cfg.LLMReasoningEffort = EffortHigh
	if got := a.roundBudget(context.Background(), Heavy, Trigger{}); got != effortBudget[EffortHigh] {
		t.Errorf("with no run effort = %s, want the node's high", got)
	}
	if got := a.roundBudget(context.Background(), Heavy, Trigger{ReasoningEffort: EffortXHigh}); got != effortBudget[EffortXHigh] {
		t.Errorf("with a run effort = %s, want the run's xhigh", got)
	}
}

// An effort nobody recognises gets the default, not zero. A deadline of zero is
// a context that has already expired.
func TestRoundBudget_AnUnknownEffortGetsTheDefault(t *testing.T) {
	a := &Agent{}
	if got := a.roundBudget(context.Background(), Heavy, Trigger{ReasoningEffort: "enthusiastic"}); got != effortBudget[EffortDefault] {
		t.Errorf("an unknown effort gives %s, want the default", got)
	}
}

// A model measured slow is given longer, on the same effort.
//
// A deadline is one number for every model, and the models are not one speed.
// Live traffic on one deployment: kimi-k2.6 passed 115 seconds on 5 of its 25
// calls, qwen3.6-35b-a3b on 4 of 711. The deadline that fits the second cuts
// the first off while it is working.
func TestRoundBudget_ASlowModelIsGivenLonger(t *testing.T) {
	a := &Agent{}
	a.cfg.Pace = func(model string) float64 {
		switch model {
		case "slowcoach":
			return 1.5
		case "glacier":
			return 2
		}
		return 1
	}
	a.llm = llm.NewClient("http://example.invalid", "k", "quick")

	ordinary := a.roundBudget(context.Background(), Heavy, Trigger{})

	a.llm = llm.NewClient("http://example.invalid", "k", "slowcoach")
	if got := a.roundBudget(context.Background(), Heavy, Trigger{}); got != ordinary*3/2 {
		t.Errorf("a slow model gets %s, want half again as long as %s", got, ordinary)
	}
	a.llm = llm.NewClient("http://example.invalid", "k", "glacier")
	if got := a.roundBudget(context.Background(), Heavy, Trigger{}); got != ordinary*2 {
		t.Errorf("a very slow model gets %s, want twice %s", got, ordinary)
	}
}

// The pace lengthens a deadline and never shortens one.
//
// A model measured FASTER than the rest is still given the whole allowance:
// finishing early costs nothing, and a deadline shortened by a measurement is a
// run cut off by an average.
func TestRoundBudget_APaceNeverTakesTimeAway(t *testing.T) {
	a := &Agent{}
	a.llm = llm.NewClient("http://example.invalid", "k", "quick")
	ordinary := a.roundBudget(context.Background(), Heavy, Trigger{})

	a.cfg.Pace = func(string) float64 { return 0.25 }
	if got := a.roundBudget(context.Background(), Heavy, Trigger{}); got != ordinary {
		t.Errorf("a fast model was given %s instead of the ordinary %s", got, ordinary)
	}
}

// No catalog, no model, no change. An engine wired to neither waits exactly as
// long as it did before any of this existed.
func TestRoundBudget_NoPaceLookupChangesNothing(t *testing.T) {
	a := &Agent{}
	if got := a.roundBudget(context.Background(), Heavy, Trigger{}); got != minRoundBudget {
		t.Errorf("with no lookup = %s, want the %s floor", got, minRoundBudget)
	}
}
