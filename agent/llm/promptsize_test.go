package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// What a prompt weighs, as capReply estimates it.
//
// The estimate decides how much room is left for the reply, so an estimate that
// is low sends a request the provider refuses outright — and the caller is told
// its context length was exceeded rather than given a shorter answer.

// A tool call's ARGUMENTS are prompt. They are the largest part of an
// aggregator's prompt and the estimate could not see them: every step's
// parameters travel on an assistant message whose Content is empty and whose
// ToolCalls carry the JSON (agent.BuildMessagesWithResults).
func TestAToolCallsArgumentsCountTowardThePrompt(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"command": strings.Repeat("x", 400_000)})
	msgs := []Message{
		{Role: "system", Content: "be helpful"},
		{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "c1", Type: "function",
			Function: FunctionCall{Name: "bash", Arguments: string(args)},
		}}},
		{Role: "tool", ToolCallID: "c1", Content: "ok"},
		{Role: "user", Content: "summarise"},
	}

	got := promptTokens(msgs)
	// 400,000 characters of arguments is ~100,000 tokens. An estimate that does
	// not see them reports a prompt of about four.
	if got < 90_000 {
		t.Errorf("promptTokens = %d for a prompt carrying 400,000 characters of tool "+
			"arguments. The arguments are sent and are not counted, so capReply "+
			"believes there is room for a reply there is no room for.", got)
	}
}

// And the consequence, through the function that actually decides.
//
// This is the shape of the live failure: an aggregator prompt that overflows the
// window on its own, and a reply cap left untouched because the overflow is
// invisible. The provider answered HTTP 400 — "maximum context length is 262144
// tokens, you requested about 271163" — and the run died rather than producing
// a short answer.
func TestAReplyIsNotSizedAgainstAPromptThatIsNotCounted(t *testing.T) {
	const window = 262_144
	args, _ := json.Marshal(map[string]string{"result": strings.Repeat("y", 1_200_000)})
	req := &ChatRequest{
		MaxTokens: 8192,
		Messages: []Message{
			{Role: "system", Content: "be helpful"},
			{Role: "assistant", ToolCalls: []ToolCall{{
				ID: "c1", Type: "function",
				Function: FunctionCall{Name: "bash", Arguments: string(args)},
			}}},
			{Role: "user", Content: "summarise"},
		},
	}
	c := NewClient("http://example.invalid", "", "m").
		Limits(func(string) (int, int) { return window, 0 })

	// 1,200,000 characters is ~300,000 tokens, which does not fit in the window
	// at all. Whatever the right reply cap is, it is not the one the caller asked
	// for — there is no room left to lower it from.
	if got := c.ReplyCap(req); got >= 8192 {
		t.Errorf("ReplyCap = %d against a %d-token window whose prompt already "+
			"overflows it. The request goes out asking for %d more tokens than the "+
			"provider will take, and comes back HTTP 400.", got, window, got)
	}
}
