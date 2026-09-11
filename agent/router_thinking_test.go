package agent

import (
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// The router answers three questions in one call now: what kind of turn this
// is, what it needs looked up, and whether it is worth reasoning about.
//
// The third was missing, so every conversational turn took the model's own
// default — which on current models is thinking. A greeting cost the same wait
// as a comparison, and nothing could say otherwise because nothing was asked.

// A missing answer is no opinion, not a no. A router that failed must not be
// able to switch thinking off for every turn after it.
func TestAMissingJudgementLeavesTheModelAlone(t *testing.T) {
	if got := wantFrom(nil); got != llm.WantAuto {
		t.Errorf("no answer gave %v, want %v", got, llm.WantAuto)
	}
	yes, no := true, false
	if got := wantFrom(&yes); got != llm.WantOn {
		t.Errorf("true gave %v, want %v", got, llm.WantOn)
	}
	if got := wantFrom(&no); got != llm.WantOff {
		t.Errorf("false gave %v, want %v", got, llm.WantOff)
	}
}

// What the stage that read the message decided beats what the lane would have
// said on its own. The lane cannot know whether a turn is worth thinking about.
func TestWhatTheRouterDecidedBeatsTheLanesOwnAnswer(t *testing.T) {
	a := effortAgent("", "")
	for _, want := range []llm.Want{llm.WantOn, llm.WantOff} {
		got := a.thinkingFor(t.Context(), Answer, &llm.Reasoning{Want: want})
		if got == nil || got.Want != want {
			t.Errorf("the lane returned %+v, want the router's %v", got, want)
		}
	}
}

// Except on the two lanes where it is not a preference. A forced routing call
// has nothing to reason about whatever anybody judged.
func TestTheSmallCallLanesOverruleTheRouter(t *testing.T) {
	a := effortAgent("", "")
	for _, l := range []Lane{Light, Route} {
		got := a.thinkingFor(t.Context(), l, &llm.Reasoning{Want: llm.WantOn})
		if got == nil || !got.Off() {
			t.Errorf("%s lane returned %+v with the router asking to think, want off", l, got)
		}
	}
}

// An effort still layers onto whatever was decided: they are separate
// questions, and one bounds thinking the other decided to have.
func TestAnEffortLayersOntoTheJudgement(t *testing.T) {
	got := effortAgent("high", "").thinkingFor(t.Context(), Answer, &llm.Reasoning{Want: llm.WantOn})
	if got == nil || got.Want != llm.WantOn || got.Effort != llm.EffortHigh {
		t.Errorf("got %+v, want on at high", got)
	}
}

// The graph answers for its preflight, and for the absence of one.
func TestTheGraphReportsWhatPreflightJudged(t *testing.T) {
	if got := (*Graph)(nil).PreflightThinking(); got != llm.WantAuto {
		t.Errorf("a nil graph gave %v", got)
	}
	g := NewGraph()
	if got := g.PreflightThinking(); got != llm.WantAuto {
		t.Errorf("a graph with no preflight gave %v, want no opinion", got)
	}
	g.Preflight = &PreflightResult{Mode: "chat", Thinking: llm.WantOff}
	if got := g.PreflightThinking(); got != llm.WantOff {
		t.Errorf("got %v, want the judgement the router made", got)
	}
}
