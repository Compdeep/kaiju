package agent

import (
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
