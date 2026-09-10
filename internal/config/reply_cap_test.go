package config

import "testing"

// The reply cap has to fit what a planner call actually spends.
//
// It was 4,096, set when a lane meant one non-reasoning model. Measured on the
// real 51KB planner prompt across 52 models, five need more than that before
// writing a single step of the plan: gpt-5-nano 7,804 (7,424 of it thinking),
// nemotron-3-nano 6,597, glm-5.3 5,623, qwen3.8-2.4t 4,735, and gpt-5 about
// 4,470.
//
// A thinking model writes its hidden tokens into this same budget, so the reply
// is what is left after it has thought — which is why the number has to clear
// the thinking and not just the plan.
func TestTheDefaultReplyCapClearsTheMeasuredWorstCase(t *testing.T) {
	const worstMeasured = 7804 // gpt-5-nano, on the planner prompt

	got := Default().LLM.MaxTokens
	if got < worstMeasured {
		t.Errorf("the default reply cap is %d; gpt-5-nano spends %d on that prompt, "+
			"so its plan would be cut off before it started", got, worstMeasured)
	}
	// Raising the ASK is safe on its own — the client trims it to the model's
	// own published ceiling before sending — but a number far above anything
	// measured is one nobody chose.
	if got > 4*worstMeasured {
		t.Errorf("the default reply cap is %d, more than four times the worst measured (%d)",
			got, worstMeasured)
	}
}

// What bounds the TIME is the deadline, not this. A generous token cap on a
// model that will not stop thinking buys a complete plan and an open-ended
// wait, which is why agent.roundBudget exists beside it.
func TestTheReplyCapIsNotATimeBound(t *testing.T) {
	if Default().LLM.MaxTokens <= 4096 {
		t.Error("the reply cap is back at the value that truncated five measured models")
	}
}
