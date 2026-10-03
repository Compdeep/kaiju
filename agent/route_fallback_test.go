package agent

import (
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// A truncated reply and a refusal are different failures and want opposite
// defaults. Routing both to chat is how a mechanical failure became a turn where
// nothing happened: chat has no tools, so the lane cannot act on whatever the
// message asked for.
func TestTruncatedReplyIsTold(t *testing.T) {
	cut := &llm.ChatResponse{Choices: []llm.Choice{{FinishReason: "length"}}}
	if !truncatedReply(cut) {
		t.Error("finish_reason length was not recognised as a truncation")
	}
	for _, r := range []string{"stop", "tool_calls", ""} {
		resp := &llm.ChatResponse{Choices: []llm.Choice{{FinishReason: r}}}
		if truncatedReply(resp) {
			t.Errorf("finish_reason %q was read as a truncation", r)
		}
	}
	if truncatedReply(nil) {
		t.Error("a nil response was read as a truncation")
	}
	// Several choices, one cut: the reply is not whole.
	mixed := &llm.ChatResponse{Choices: []llm.Choice{{FinishReason: "stop"}, {FinishReason: "length"}}}
	if !truncatedReply(mixed) {
		t.Error("a truncated choice among whole ones was missed")
	}
}
