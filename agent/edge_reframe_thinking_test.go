package agent

import (
	"context"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

/*
 * The reframe edge asks for no thinking.
 *
 * It has no judgement to make: the nodes have run and its job is to form what
 * they produced for the next stage to read. Left at the provider's default,
 * which means thinking is on, it spent the whole reply budget reasoning and
 * returned an empty content field — and the edge then fell back to passing the
 * material through unformed.
 *
 * Asserted on the shape rather than through a call because the failure is a
 * field nobody set: nothing in ask or prepare touches Think, so an edge that
 * does not ask gets whatever the provider does.
 */
func TestTheReframeEdgeAsksForNoThinking(t *testing.T) {
	req := llm.WithoutReasoning(&llm.ChatRequest{})
	if req.Think == nil {
		t.Fatal("WithoutReasoning left Think nil — the edge would take the provider default")
	}
	if req.Think.Want != llm.WantOff {
		t.Errorf("Want = %v, want WantOff", req.Think.Want)
	}
}

// And the opposite door stays open: a stage that weighs evidence keeps its
// thinking, so turning it off here must not be mistaken for turning it off
// everywhere.
func TestReasoningCanStillBeAskedFor(t *testing.T) {
	req := llm.WithReasoning(&llm.ChatRequest{})
	if req.Think == nil || req.Think.Want != llm.WantOn {
		t.Errorf("WithReasoning gave %+v, want WantOn", req.Think)
	}
}

/*
 * And the cap covers the reasoning when the OFF cannot be sent.
 *
 * The request above is honoured only where the application's catalog says the
 * model can stop reasoning; where it says nothing, resolve declines to send an
 * instruction it cannot know will be honoured and the edge reasons anyway. The
 * cap is then the only thing standing between the reasoning and the paragraph.
 *
 * Measured on qwen/qwen3.6-35b-a3b at the old 2,000: one framing in three cut
 * off mid-sentence, and the recovery wrote its answer off the tail. The two
 * numbers below are the whole of the fix — a 262,144-token window reaching
 * 2,048 for a model that does not reason, doubled to the ceiling for one that
 * does.
 */
func TestTheEdgeCapLeavesRoomForReasoningItCannotSwitchOff(t *testing.T) {
	const window = 262144

	agentOn := edgeCapAgent(window, true)
	if got := agentOn.replyBudget(context.Background(), Light, replyEdgeBudget); got != 4096 {
		t.Errorf("a reasoning model got %d tokens for reasoning and a paragraph, want 4096", got)
	}

	agentOff := edgeCapAgent(window, false)
	if got := agentOff.replyBudget(context.Background(), Light, replyEdgeBudget); got != 2048 {
		t.Errorf("a model that does not reason got %d, want 2048 — the paragraph's p90 is 242", got)
	}

	// A deployment that supplies no catalog keeps the number this engine was
	// measured at. Raising a ceiling must not move anyone who never asked.
	if got := (&Agent{}).replyBudget(context.Background(), Light, replyEdgeBudget); got != replyEdgeBudget.Base {
		t.Errorf("with no catalog the edge got %d, want the base %d", got, replyEdgeBudget.Base)
	}
}

// An agent whose executor lane — the lane every edge runs on — reaches one
// model of the given window, which reasons or does not.
func edgeCapAgent(window int, thinks bool) *Agent {
	a := &Agent{}
	a.executor = llm.NewClient("http://example.invalid", "", "some/model")
	a.cfg.Limits = func(string) (int, int) { return window, 0 }
	a.cfg.Thinks = func(string) bool { return thinks }
	return a
}
