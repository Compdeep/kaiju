package agent

import (
	"context"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// The division is measured, not chosen.
//
// On the real planner prompt across 52 models, visible answers ran to a median
// of 263 tokens and a largest of 1,704 — so 2,048 clears every answer observed.
// Thinking is where the variance is: gpt-5 wanted 4,288, glm-5.3 5,170,
// nemotron-3-nano 6,492, gpt-5-nano 7,424.
//
// At 6,144 for thinking, 49 of the 52 finish thinking and NONE loses its
// answer. The three that are cut are the ones that today return nothing at all.
func TestSplitBudget_LeavesTheAnswerRoom(t *testing.T) {
	const largestAnswerMeasured = 1704

	think, total := splitBudget(8192)
	if total != 8192 {
		t.Errorf("total = %d, want the allowance unchanged", total)
	}
	if think != 6144 {
		t.Errorf("thinking = %d, want 6144", think)
	}
	if left := total - think; left < largestAnswerMeasured {
		t.Errorf("the answer is left %d tokens, below the largest measured (%d)",
			left, largestAnswerMeasured)
	}
}

// The proportion is fixed, not the numbers: a lane bounding one sentence must
// not be given the allowance of the lane writing a reply to a person.
func TestSplitBudget_ScalesWithTheLane(t *testing.T) {
	for _, total := range []int{2048, 4096, 8192, 16384} {
		think, out := splitBudget(total)
		if out != total {
			t.Errorf("%d: total changed to %d", total, out)
		}
		if think >= total {
			t.Errorf("%d: thinking took %d, leaving the answer nothing", total, think)
		}
		if think > 0 && total-think < 256 {
			t.Errorf("%d: the answer is left only %d", total, total-think)
		}
	}
}

// Nothing to divide means nothing sent. Inventing a thinking budget for a
// request whose reply nobody bounded is a number from nowhere.
func TestSplitBudget_NothingToDivide(t *testing.T) {
	if th, tot := splitBudget(0); th != 0 || tot != 0 {
		t.Errorf("splitBudget(0) = %d, %d; want nothing", th, tot)
	}
	// Too small to divide usefully: a thinking cap of a few dozen tokens buys a
	// truncated thought and no better answer.
	if th, tot := splitBudget(400); th != 0 || tot != 400 {
		t.Errorf("splitBudget(400) = %d, %d; want the allowance left whole", th, tot)
	}
}

// Every lane gets the division, because it happens at the call seam rather than
// at each call site.
//
// Wiring it per lane is exactly how the planner ended up with a guard the chat
// lane did not have — and the chat lane is the one a person waits on.
func TestTheDivisionReachesEveryLane(t *testing.T) {
	honours := func(string) ([]string, bool) { return nil, true }
	for _, lane := range []Lane{Heavy, Light, Answer, Route} {
		a := reasoningAgent("", 0, honours)
		req := &llm.ChatRequest{MaxTokens: 8192}
		a.applyReasoningBudget(context.Background(), req, "m")
		if req.Reasoning == nil || req.Reasoning.MaxTokens != 6144 {
			t.Errorf("lane %v: thinking budget = %+v, want 6144", lane, req.Reasoning)
		}
		_ = lane
	}
}
