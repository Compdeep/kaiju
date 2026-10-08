package agent

import (
	"context"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

/*
 * Every reframe edge states its thinking, and only the reflector's reasons.
 *
 * Both halves matter. An edge that states nothing takes the provider's default,
 * which on every model line since early 2026 is ON — and this edge then spent
 * the whole reply budget reasoning and returned an empty content field, so the
 * only thing recoverable was the tail of an unfinished argument. And the edge
 * that feeds the reflector is the one whose reader acts on what it says, so it
 * is the one where the reasoning is worth paying for.
 *
 * Asserted on the shape rather than through a call because the failure was a
 * field nobody set: nothing in ask or prepare touches Think.
 */
func TestOnlyTheReflectorEdgeReasons(t *testing.T) {
	for _, tc := range []struct {
		edge ReframeEdge
		want llm.Want
	}{
		{ReframeToPlanner, llm.WantOff},
		{ReframeToReflector, llm.WantOn},
		{ReframeToAnswer, llm.WantOff},
	} {
		req := tc.edge.reasoning(&llm.ChatRequest{})
		if req.Think == nil {
			t.Fatalf("%s left Think nil — it would take the provider default", tc.edge.Name)
		}
		if req.Think.Want != tc.want {
			t.Errorf("%s: Want = %v, want %v", tc.edge.Name, req.Think.Want, tc.want)
		}
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
 * One real framing payload replayed 30 times on qwen/qwen3.6-35b-a3b spent
 * between 957 and 3,465 tokens reasoning, median 2,257, and four of the thirty
 * spent the whole cap and returned nothing. A 2,000 cap sat under 20 of those
 * 30 and a 4,096 cap under 4; nothing in the thirty reached 5,000. The two
 * numbers below are the whole of the fix — a 262,144-token window reaching
 * 4,096 for a model that does not reason, doubled to the ceiling for one that
 * does.
 */
func TestTheEdgeCapLeavesRoomForReasoningItCannotSwitchOff(t *testing.T) {
	const window = 262144

	agentOn := edgeCapAgent(window, true)
	if got := agentOn.replyBudget(context.Background(), Light, replyEdgeBudget); got != 8192 {
		t.Errorf("a reasoning model got %d tokens for reasoning and a paragraph, want 8192", got)
	}

	agentOff := edgeCapAgent(window, false)
	if got := agentOff.replyBudget(context.Background(), Light, replyEdgeBudget); got != 4096 {
		t.Errorf("a model that does not reason got %d, want 4096 — the paragraph's p90 is 242", got)
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
