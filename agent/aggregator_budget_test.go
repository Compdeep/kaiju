package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// A stream that stops at the cap with nothing written is a budget spent
// reasoning, and it used to be reported as "empty response" — true, and no help
// at all, on a run that had taken seventeen minutes to get there.
//
// A PARTIAL reply is still not failed: the text has already been shown to
// whoever asked, and there is nothing to gain by throwing it away.
//
// A reply with NOTHING in it is no longer reported at all on the first attempt.
// The door asks again with thinking off before the caller ever sees it, which is
// the whole remedy this fault has: the budget went on reasoning, so a model that
// cannot reason has nothing to spend it on but the answer.
func TestAskStream_CutWithNothingWrittenIsAskedAgain(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{"": {Content: "the answer, second time"}})
	model.answerNth("", stubReply{Content: "", Cut: true})
	a := agentOnStub(t, model)

	text, err := a.askStream(context.Background(), Answer,
		&llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "summarise"}}, MaxTokens: 64},
		func(string, string) {})

	if err != nil {
		t.Fatalf("a reply that produced nothing was reported rather than re-asked: %v", err)
	}
	if !strings.Contains(text, "second time") {
		t.Errorf("the recovered answer did not reach the caller: %q", text)
	}
}

// And when the second attempt produces nothing either, what comes back still
// reads as a reply that ran out of budget.
//
// ErrNoReply wraps llm.ErrReplyTruncated on purpose: the stages that already
// tell "ran out of budget" from "the provider failed" — the aggregator is one —
// keep working without learning a second name for the same thing.
func TestAskStream_NothingTwiceStillReadsAsTruncation(t *testing.T) {
	model := newStubModel(t, map[string]stubReply{"": {Content: "", Cut: true}})
	a := agentOnStub(t, model)

	text, err := a.askStream(context.Background(), Answer,
		&llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "summarise"}}, MaxTokens: 64},
		func(string, string) {})

	if !errors.Is(err, llm.ErrReplyTruncated) {
		t.Fatalf("err = %v, want it to read as ErrReplyTruncated", err)
	}
	if !errors.Is(err, ErrNoReply) {
		t.Errorf("err = %v, want ErrNoReply — it says which of the two this was", err)
	}
	if text != "" {
		t.Errorf("text = %q, want empty", text)
	}
}

// Cut, but something was written. That reaches the caller as it always did.
func TestAskStream_CutWithTextKeepsTheText(t *testing.T) {
	model := newStubModel(t, nil)
	model.answerNth("", stubReply{Content: "as far as it got", Cut: true})
	a := agentOnStub(t, model)

	text, err := a.askStream(context.Background(), Answer,
		&llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "summarise"}}, MaxTokens: 64},
		func(string, string) {})

	if err != nil {
		t.Fatalf("a partial stream was failed: %v", err)
	}
	if !strings.Contains(text, "as far as it got") {
		t.Errorf("the partial text was lost: %q", text)
	}
}

// An ordinary reply is unchanged.
func TestAskStream_CompleteReplyIsUnchanged(t *testing.T) {
	model := newStubModel(t, nil)
	model.answerNth("", stubReply{Content: "the whole answer"})
	a := agentOnStub(t, model)

	text, err := a.askStream(context.Background(), Answer,
		&llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "summarise"}}, MaxTokens: 64},
		func(string, string) {})

	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(text, "the whole answer") {
		t.Errorf("text = %q", text)
	}
}

// The aggregator's budget asks whether the model reasons.
//
// It doubled the configured cap for everyone: an aggregator reads every step
// and writes the whole reply, so it needs more room than a tool call. That is
// the work, and it is the same for every model. The reasoning is not — it comes
// out of this same budget, and a model that reasons was handed a non-reasoning
// model's allowance, spent all of it thinking, and returned nothing.
//
// planMaxTokens already asks this, and wallClock already widens the clock on
// the same grounds. This was the third place that had to know.
func TestAggregatorBudget_DoublesAgainForAThinkingModel(t *testing.T) {
	plain := &Agent{cfg: Config{ModelConfig: ModelConfig{MaxTokens: 8000}}}
	if got := plain.heavyThinks("stub"); got {
		t.Fatalf("a config with no Thinks reported thinking")
	}

	thinker := &Agent{cfg: Config{
		ModelConfig: ModelConfig{MaxTokens: 8000, Thinks: func(string) bool { return true }},
	}}
	if !thinker.heavyThinks("stub") {
		t.Fatal("a model the catalog says reasons was not recognised")
	}
}

// The override wins over the catalog, both ways — an operator who turned
// reasoning off must not be given the reasoning budget.
func TestAggregatorBudget_HonoursTheReasoningOverride(t *testing.T) {
	a := &Agent{
		cfg:          Config{ModelConfig: ModelConfig{MaxTokens: 8000, Thinks: func(string) bool { return true }}},
		llmReasoning: "off",
	}
	if a.heavyThinks("stub") {
		t.Error("reasoning is off, so the budget must not be doubled for it")
	}
	a.llmReasoning = "on"
	a.cfg.ModelConfig.Thinks = func(string) bool { return false }
	if !a.heavyThinks("stub") {
		t.Error("reasoning is on, so the budget must be doubled whatever the catalog says")
	}
}
