package agent

import (
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/models"
)

// The two lanes that force a SMALL call get thinking turned off, whatever model
// is configured. Not a default and not a setting: there is no deployment in
// which hidden reasoning helps a 96-token routing decision.
func TestTheSmallForcedCallLanesTurnReasoningOff(t *testing.T) {
	for _, l := range []Lane{Light, Route} {
		req := &llm.ChatRequest{}
		a := &Agent{llm: llm.NewClient("http://127.0.0.1:1", "", "m"),
			executor: llm.NewClient("http://127.0.0.1:1", "", "m")}
		a.prepare(t.Context(), l, req)
		if req.Reasoning == nil {
			t.Errorf("%s lane left reasoning at the provider's default, which is ON", l)
			continue
		}
		if req.Reasoning.Enabled {
			t.Errorf("%s lane asked for reasoning ON", l)
		}
	}
}

// Heavy and Answer are deliberately untouched. Heavy forces a call too, but it
// has the budget and the thinking earns it — on one planner prompt a thinking
// model returned complete plans three times of three where a non-thinking one
// ran the cap out three times of three. Answer writes prose, where it is better.
func TestTheReasoningAndAnswerLanesAreLeftAlone(t *testing.T) {
	for _, l := range []Lane{Heavy, Answer} {
		req := &llm.ChatRequest{}
		a := &Agent{llm: llm.NewClient("http://127.0.0.1:1", "", "m")}
		a.prepare(t.Context(), l, req)
		if req.Reasoning != nil {
			t.Errorf("%s lane overrode the operator's model choice", l)
		}
	}
}

// The picker's half of the rule: offer what can emit the call.
//
// It briefly excluded thinking models too, and the measurement killed that. Run
// against three real preflight prompts with reasoning off, the three best were
// all reason-capable, and the non-thinking model that had been the default was
// 25 times slower than the winner and cut at the cap on one prompt of three. The
// lane turns reasoning off before it sends, so what a model would do if allowed
// to think decides nothing here.
func TestTheForcedSmallCallListOffersWhatCanEmitTheCall(t *testing.T) {
	list := models.ForcedSmallCall()
	if len(list) == 0 {
		t.Fatal("no model is fit for a forced small call; both pickers show nothing")
	}
	for _, m := range list {
		if !m.ToolCallOK || !m.Tools {
			t.Errorf("%q is offered without the flags that qualify it", m.ID)
		}
	}
	// A reasoning model has to be reachable, or the winners of the bench are not.
	thinkers := 0
	for _, m := range list {
		if m.Thinks() {
			thinkers++
		}
	}
	if thinkers == 0 {
		t.Error("no reasoning model is offered; the filter is excluding on Thinking again")
	}
	// And a model measured unfit must not be, or the measurement bought nothing.
	if _, ok := models.Find("qwen/qwen3-30b-a3b-instruct-2507"); ok {
		for _, m := range list {
			if m.ID == "qwen/qwen3-30b-a3b-instruct-2507" {
				t.Error("a model measured as cut at the cap is still offered")
			}
		}
	}
}
