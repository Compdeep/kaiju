package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// A reply that ran long and one that never started both say "length", and the
// remedies are opposite: one needs a shorter plan, the other needs the thinking
// stopped. Asking a model that wrote NOTHING to write less answers a question
// it was not asked.
func TestNothingVisible_TellsTheTwoCutsApart(t *testing.T) {
	if !nothingVisible(llm.Choice{FinishReason: "length"}) {
		t.Error("a reply with no tool call and no content was not read as empty")
	}
	if !nothingVisible(llm.Choice{Message: llm.Message{Content: "  \n "}}) {
		t.Error("whitespace was read as output")
	}
	// Reasoning is what consumed the budget, not what the budget was for.
	// Counting it as output is the confusion this exists to prevent.
	if !nothingVisible(llm.Choice{Message: llm.Message{Reasoning: "I should start by…"}}) {
		t.Error("reasoning was read as output")
	}
	if nothingVisible(llm.Choice{Message: llm.Message{Content: `{"steps":[`}}) {
		t.Error("a plan cut mid-JSON was read as empty; it has something to salvage")
	}
	if nothingVisible(llm.Choice{Message: llm.Message{ToolCalls: []llm.ToolCall{{ID: "1"}}}}) {
		t.Error("a tool call was read as empty")
	}
}

// The recovery hands back the model's own thinking, and says it was cut off.
//
// Handed back as a finished argument, a model treats a half-formed conclusion
// as settled and plans from it. Told it was cut off, it closes the thought.
func TestRecoveryPrompt_ReturnsTheThinkingAsUnfinished(t *testing.T) {
	p := recoveryPrompt("first I will geocode each city")
	for _, want := range []string{"Previous reasoning", "first I will geocode each city", "cut off", "Do not think further"} {
		if !strings.Contains(p, want) {
			t.Errorf("the recovery prompt does not mention %q:\n%s", want, p)
		}
	}
}

// A provider that reports reasoning tokens but returns no reasoning TEXT is the
// ordinary case on several models. The recovery is still worth making there —
// it is then a retry that cannot think — so the prompt must stand on its own.
func TestRecoveryPrompt_WorksWithNoReasoningText(t *testing.T) {
	p := recoveryPrompt("")
	if strings.Contains(p, "Previous reasoning") {
		t.Error("an empty reasoning block was included")
	}
	if !strings.Contains(p, "Do not think further") {
		t.Error("the instruction to stop thinking was lost when there was no reasoning")
	}
}

// The end of a cut-off thought is the part worth keeping: a model stopped
// mid-sentence was closest to an answer at the end, not at the beginning.
func TestRecoveryPrompt_KeepsTheEndOfALongThought(t *testing.T) {
	long := strings.Repeat("x", maxRecoveredReasoning+500) + "THE-LAST-THING-IT-THOUGHT"
	p := recoveryPrompt(long)
	if !strings.Contains(p, "THE-LAST-THING-IT-THOUGHT") {
		t.Error("the end of the reasoning was trimmed away, which is the part that matters")
	}
	if len(p) > maxRecoveredReasoning+1000 {
		t.Errorf("the recovery prompt is %d chars; it should be bounded", len(p))
	}
}

// The retry asks for the same thing, without thinking, carrying what was
// thought — and does not disturb the request it retries.
func TestWithoutThinkingRetry_AsksAgainWithoutThinking(t *testing.T) {
	req := &llm.ChatRequest{
		Messages:   []llm.Message{{Role: "user", Content: "plan it"}},
		MaxTokens:  16384,
		ToolChoice: llm.ForceToolChoice("plan"),
	}
	before := len(req.Messages)
	cut := &llm.ChatResponse{Choices: []llm.Choice{{
		FinishReason: "length", Message: llm.Message{Reasoning: "thinking…"}}}}

	retry := withoutThinkingRetry(req, cut)

	if len(req.Messages) != before {
		t.Errorf("the original request grew from %d to %d messages", before, len(req.Messages))
	}
	if req.Reasoning != nil {
		t.Error("the original request had thinking switched off on it")
	}
	if retry.Reasoning == nil || retry.Reasoning.On() {
		t.Errorf("the retry does not have thinking off: %+v", retry.Reasoning)
	}
	if retry.MaxTokens != req.MaxTokens || retry.ToolChoice == nil {
		t.Error("the retry changed something other than the thinking and the messages")
	}
	last := retry.Messages[len(retry.Messages)-1]
	if last.Role != "user" {
		t.Errorf("the recovery text was sent as %q; some providers refuse an assistant "+
			"message carrying reasoning with no matching tool call", last.Role)
	}
	if !strings.Contains(last.Content, "thinking…") {
		t.Error("the retry did not carry the reasoning it recovered")
	}
}

// Nothing to recover from is not a crash.
func TestWithoutThinkingRetry_HandlesNothing(t *testing.T) {
	if withoutThinkingRetry(nil, nil) != nil {
		t.Error("a nil request produced a retry")
	}
	r := withoutThinkingRetry(&llm.ChatRequest{}, nil)
	if r == nil || len(r.Messages) != 1 {
		t.Error("a cut reply that carried no reasoning produced no retry")
	}
}
