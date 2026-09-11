package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// However a model delivers its thinking, a caller reads it in one place.
//
// Three deliveries exist: a reasoning field, reasoning chunks on a stream, and
// <think> written into the answer itself. The last was lifted on the streamed
// path only, so the same model's thinking landed on Message.Reasoning when
// streamed and in the middle of the answer when not — and a caller showing the
// answer showed the thinking with it.

func serving(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "", "m")
}

func TestInlineThinkingIsLiftedOnAReplyThatDidNotStream(t *testing.T) {
	c := serving(t, `{"choices":[{"message":{"content":"<think>weighing it up</think>the answer"},
		"finish_reason":"stop"}]}`)

	resp, err := c.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	got := resp.Choices[0].Message
	if got.Content != "the answer" {
		t.Errorf("content = %q, want the thinking taken out of it", got.Content)
	}
	if !strings.Contains(got.Reasoning, "weighing it up") {
		t.Errorf("reasoning = %q, want the lifted thinking", got.Reasoning)
	}
}

// And where the provider used both, neither is lost.
func TestInlineThinkingJoinsWhatTheProviderAlreadySaid(t *testing.T) {
	c := serving(t, `{"choices":[{"message":{"content":"<think>second</think>answer",
		"reasoning":"first"},"finish_reason":"stop"}]}`)

	resp, err := c.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	r := resp.Choices[0].Message.Reasoning
	if !strings.Contains(r, "first") || !strings.Contains(r, "second") {
		t.Errorf("reasoning = %q, want both what the field carried and what was lifted", r)
	}
}

// An answer with no thinking in it is untouched.
func TestAnAnswerWithoutThinkingIsUnchanged(t *testing.T) {
	c := serving(t, `{"choices":[{"message":{"content":"just the answer"},"finish_reason":"stop"}]}`)
	resp, err := c.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got := resp.Choices[0].Message; got.Content != "just the answer" || got.Reasoning != "" {
		t.Errorf("content=%q reasoning=%q, want the reply left alone", got.Content, got.Reasoning)
	}
}

// What the thinking cost is billed inside the completion tokens, so without the
// breakdown it cannot be told apart from the answer.
func TestWhatTheThinkingCostIsReadable(t *testing.T) {
	c := serving(t, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":100,"completion_tokens":900,"total_tokens":1000,
		         "completion_tokens_details":{"reasoning_tokens":800}}}`)

	resp, err := c.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got := resp.Usage.ReasoningTokens(); got != 800 {
		t.Errorf("reasoning tokens = %d, want 800", got)
	}
	if resp.Usage.CompletionTokens != 900 {
		t.Errorf("completion = %d; the reasoning is PART of it, not additional",
			resp.Usage.CompletionTokens)
	}
}

// A provider that says nothing about the split reports nothing, rather than
// zero-as-a-fact.
func TestAProviderThatDoesNotSplitReportsNothing(t *testing.T) {
	c := serving(t, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`)

	resp, err := c.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Usage.Details != nil {
		t.Errorf("a breakdown was invented: %+v", resp.Usage.Details)
	}
	if got := resp.Usage.ReasoningTokens(); got != 0 {
		t.Errorf("reasoning tokens = %d, want 0 for a provider that did not say", got)
	}
}
