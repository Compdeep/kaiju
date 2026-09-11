package agent

import (
	"context"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// The precedence, asserted as an order rather than as a set of cases: the
// lane's own rule, then the run's choice, then the operator's, then nothing.

func effortAgent(nodeEffort, reasoning string) *Agent {
	a := &Agent{cfg: Config{ModelConfig: ModelConfig{LLMReasoningEffort: nodeEffort}}}
	a.llmReasoning = reasoning
	return a
}

// The two lanes that force a small call refuse thinking whatever anybody set.
// Not a default and not a setting — there is nothing here for an operator to
// decide.
func TestTheSmallCallLanesRefuseThinkingWhateverIsSet(t *testing.T) {
	for _, l := range []Lane{Light, Route} {
		for _, reasoning := range []string{"", "on", "off"} {
			a := effortAgent("max", reasoning)
			got := a.thinkingFor(context.Background(), l, nil)
			if got == nil || !got.Off() {
				t.Errorf("%s lane with reasoning %q asked for %+v, want thinking off", l, reasoning, got)
			}
		}
	}
}

// Heavy is the one lane an operator has a view on.
func TestTheOperatorsSwitchReachesTheReasoningLane(t *testing.T) {
	for _, c := range []struct {
		setting string
		want    llm.Want
	}{{"on", llm.WantOn}, {"off", llm.WantOff}} {
		got := effortAgent("", c.setting).thinkingFor(context.Background(), Heavy, nil)
		if got == nil || got.Want != c.want {
			t.Errorf("reasoning %q gave %+v, want %v", c.setting, got, c.want)
		}
	}
}

// Saying nothing is a real answer and the common one: it leaves the model's own
// default alone, which a stage with no opinion must not be able to change.
func TestNothingSetAsksNothing(t *testing.T) {
	for _, l := range []Lane{Heavy, Answer} {
		if got := effortAgent("", "").thinkingFor(context.Background(), l, nil); got != nil {
			t.Errorf("%s lane asked for %+v with nothing configured, want nothing said", l, got)
		}
	}
}

// A run's own effort beats the node's, and reaches the lane a person talks to.
func TestTheRunsEffortWins(t *testing.T) {
	a := effortAgent("low", "")
	ctx := withLaneSelection(context.Background(), laneSelection{effort: llm.EffortHigh})
	for _, l := range []Lane{Heavy, Answer} {
		got := a.thinkingFor(ctx, l, nil)
		if got == nil || got.Effort != llm.EffortHigh {
			t.Errorf("%s lane used %+v, want the run's high over the node's low", l, got)
		}
	}
}

// And the node's applies when the run says nothing.
func TestTheNodesEffortAppliesWhenTheRunSaysNothing(t *testing.T) {
	got := effortAgent("xhigh", "").thinkingFor(context.Background(), Answer, nil)
	if got == nil || got.Effort != llm.EffortXHigh {
		t.Errorf("got %+v, want the node's xhigh", got)
	}
	// An effort alone says nothing about WHETHER to think: it bounds thinking
	// that was already going to happen.
	if got.Want != llm.WantAuto {
		t.Errorf("an effort alone decided Want=%v; it must leave that question alone", got.Want)
	}
}

// A mistyped setting leaves the model alone rather than refusing the run.
func TestAnUnrecognisedEffortIsIgnored(t *testing.T) {
	if got := effortAgent("enthusiastic", "").thinkingFor(context.Background(), Answer, nil); got != nil {
		t.Errorf("got %+v, want nothing said for a word nobody can send", got)
	}
}

// A run's effort reaches the trigger the API boundary builds.
func TestATriggersEffortReachesTheLaneSelection(t *testing.T) {
	sel := laneSelectionFromTrigger(Trigger{ReasoningEffort: "max"})
	if sel.effort != llm.EffortMax {
		t.Errorf("effort = %v, want max", sel.effort)
	}
	if sel := laneSelectionFromTrigger(Trigger{ReasoningEffort: "sideways"}); sel.effort != llm.EffortUnset {
		t.Errorf("effort = %v, want unset for a word nobody can send", sel.effort)
	}
}
