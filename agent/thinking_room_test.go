package agent

import (
	"context"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// max_tokens bounds the whole completion and reasoning_tokens is a subset of it,
// so a thinking model spends the same budget twice: once working out what to write
// and once writing it.
//
// The caps were measured from replies — the coder's p90 is 1008 tokens of content
// against a cap of 16384 — and none of them accounted for the thinking. A coder on
// kimi-k2.7-code spent 12,026 of 16,384 thinking, leaving 4,358 for the JSON, and
// the reply was cut mid-structure. Twice, over 173 seconds.

func agentThatThinks(thinks bool) *Agent {
	a := &Agent{cfg: Config{ModelConfig: ModelConfig{
		Thinks:   func(string) bool { return thinks },
		LLMModel: "m",
	}}}
	// A lane with no provider registered falls back to this client, and
	// resolvedModel then takes the model from it — which is how the model reaches
	// the budget on a real run.
	a.llm = llm.NewClient("", "", "m")
	// lightLane returns a.executor, and an unconfigured one resolves to no model
	// at all — so the executor lane needs its own client for the budget to see a
	// model there.
	a.executor = llm.NewClient("", "", "m")
	return a
}

// The failing case: the coder's cap gets room for the thinking.
func TestThinkingRoom_TheCoderCapDoublesForAThinkingModel(t *testing.T) {
	flat := agentThatThinks(false).replyBudget(context.Background(), Heavy, replyCodeBudget)
	deep := agentThatThinks(true).replyBudget(context.Background(), Heavy, replyCodeBudget)

	if flat != replyCodeBudget.Base {
		t.Fatalf("a model that does not think got %d, want its Base %d", flat, replyCodeBudget.Base)
	}
	if deep <= flat {
		t.Fatalf("a thinking model got %d, no more than the %d a non-thinking one gets — "+
			"the thinking still comes out of the reply", deep, flat)
	}
	// Enough for the measured case: 12,026 thought plus a reply.
	if deep < 12026+2000 {
		t.Errorf("cap = %d, which the observed 12,026 thinking tokens would still nearly fill", deep)
	}
}

// Ceiling binds, so this cannot grow without limit.
func TestThinkingRoom_NeverPastTheCeiling(t *testing.T) {
	spec := budgetSpec{Base: 1000, Share: 16, Ceiling: 1200}
	got := agentThatThinks(true).replyBudget(context.Background(), Heavy, spec)
	if got > spec.Ceiling {
		t.Errorf("cap = %d, past the ceiling %d", got, spec.Ceiling)
	}
}

// A spec whose Base is its Ceiling does not move, which is right for a stage
// that forces reasoning off.
func TestThinkingRoom_AFixedSpecDoesNotMove(t *testing.T) {
	spec := budgetSpec{Base: 1024, Share: 16, Ceiling: 1024}
	if got := agentThatThinks(true).replyBudget(context.Background(), Heavy, spec); got != 1024 {
		t.Errorf("cap = %d, want it pinned at 1024", got)
	}
}

// The llmReasoning override belongs to the reasoning lane. ask.go:302 applies it
// only when the lane is Heavy, and the budget has to agree — an operator forcing
// reasoning off must not shrink a different lane whose model thinks regardless.
func TestThinkingRoom_TheOverrideIsOnlyTheReasoningLanes(t *testing.T) {
	a := agentThatThinks(true)
	a.llmReasoning = "off"

	heavy := a.replyBudget(context.Background(), Heavy, replyCodeBudget)
	if heavy != replyCodeBudget.Base {
		t.Errorf("Heavy with reasoning forced off got %d, want its Base %d", heavy, replyCodeBudget.Base)
	}

	light := a.replyBudget(context.Background(), Light, replyCodeBudget)
	if light <= replyCodeBudget.Base {
		t.Errorf("the executor lane got %d; the override is not that lane's to apply, "+
			"and its model thinks", light)
	}
}

// Forcing reasoning on widens it even for a model the catalog says does not think.
func TestThinkingRoom_ForcingReasoningOnWidensIt(t *testing.T) {
	a := agentThatThinks(false)
	a.llmReasoning = "on"
	if got := a.replyBudget(context.Background(), Heavy, replyCodeBudget); got <= replyCodeBudget.Base {
		t.Errorf("cap = %d; reasoning was switched on and the budget did not follow", got)
	}
}

// A nil agent still answers, because every caller reads this on a path that must
// not panic to produce a cap.
func TestThinkingRoom_NilAgentStillAnswers(t *testing.T) {
	var a *Agent
	if got := a.replyBudget(context.Background(), Heavy, replyCodeBudget); got != replyCodeBudget.Base {
		t.Errorf("got %d, want Base", got)
	}
}
