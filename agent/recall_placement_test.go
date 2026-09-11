package agent

import (
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// Both conversational lanes place a recalled block in the same spot: as a
// system message immediately before the message it was recalled for.
//
// One lane did that and the other appended it to the system prompt, which puts
// it at the top — where it reads as something said at the start of the
// conversation and long since moved past. That is the position withRecall
// exists to avoid, and two lanes assembling their own prompt is how one of them
// came to use it.
func TestARecalledBlockSitsBeforeTheMessageItWasRecalledFor(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "persona"},
		{Role: "user", Content: "the earlier one"},
		{Role: "assistant", Content: "an answer"},
		{Role: "user", Content: "which invoice did we settle on?"},
	}
	got := withRecall(msgs, "## Recalled\nthe March invoice")

	if len(got) != len(msgs)+1 {
		t.Fatalf("%d messages, want %d", len(got), len(msgs)+1)
	}
	block := got[len(got)-2]
	if block.Role != "system" || block.Content != "## Recalled\nthe March invoice" {
		t.Errorf("the block landed as %+v, want it immediately before the last message", block)
	}
	if last := got[len(got)-1]; last.Content != "which invoice did we settle on?" {
		t.Errorf("the message it was recalled for is no longer last: %q", last.Content)
	}
	if got[0].Content != "persona" {
		t.Errorf("the block displaced the system prompt: %q", got[0].Content)
	}
}

// Nothing recalled changes nothing.
func TestNothingRecalledLeavesThePromptAlone(t *testing.T) {
	msgs := []llm.Message{{Role: "user", Content: "hello"}}
	if got := withRecall(msgs, ""); len(got) != 1 || got[0].Content != "hello" {
		t.Errorf("an empty block altered the prompt: %+v", got)
	}
}
