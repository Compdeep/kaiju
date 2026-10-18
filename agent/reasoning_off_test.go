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
		if req.Think == nil {
			t.Errorf("%s lane left reasoning at the provider's default, which is ON", l)
			continue
		}
		if !req.Think.Off() {
			t.Errorf("%s lane asked for reasoning %v, want it switched off", l, req.Think.Want)
		}
	}
}

// Answer is deliberately untouched: it writes prose, where the model's own
// default is better. Heavy asks for thinking. It forces a call too, but it has
// the budget and the thinking earns it — on one planner prompt a thinking model
// returned complete plans three times of three where a non-thinking one ran the
// cap out three times of three — and left alone it would NOT be left alone: the
// client reads a forced shape with no word from the lane as a call that must
// not think, and sends the off switch.
func TestTheReasoningAndAnswerLanesAreLeftAlone(t *testing.T) {
	req := &llm.ChatRequest{}
	a := &Agent{llm: llm.NewClient("http://127.0.0.1:1", "", "m")}
	a.prepare(t.Context(), Answer, req)
	if req.Think != nil {
		t.Errorf("Answer lane overrode the operator's model choice with %+v", req.Think)
	}

	req = &llm.ChatRequest{}
	a.prepare(t.Context(), Heavy, req)
	if !req.Think.On() {
		t.Errorf("Heavy lane asked for %+v, want thinking on", req.Think)
	}
}

// The picker's half of the same rule. Two doors, because a config file reaches a
// lane without passing a picker — which is how a thinking model drove one
// deployment's executor for seven days.
//
// The rule is about reasoning that CANNOT be switched off, not reasoning that
// happens to be on. The lane above sends it off and a hybrid model obeys, so
// excluding one here would refuse a choice the engine already neutralises —
// and would empty both pickers, since most of the catalog now reasons by
// default.
func TestTheForcedSmallCallListExcludesMandatoryThinkers(t *testing.T) {
	list := models.ForcedSmallCall()
	if len(list) == 0 {
		t.Fatal("no model is fit for a forced small call; both pickers show nothing")
	}
	for _, m := range list {
		if m.Thinks() && !m.ReasoningOptional {
			t.Errorf("%q cannot be told to stop reasoning and is still offered for the executor and router", m.ID)
		}
		if !m.ToolCallOK || !m.Tools {
			t.Errorf("%q is offered without the flags that qualify it", m.ID)
		}
	}
	// And it must be narrower than the general list, or it is not filtering.
	if len(list) >= len(models.ToolSafe()) {
		t.Error("the forced-small-call list is not narrower than ToolSafe")
	}
}
