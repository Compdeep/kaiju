package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// replyServer answers one completion with the given finish reason and content.
func replyServer(t *testing.T, finish, content string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": content},
				"finish_reason": finish,
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func TestCompleteChecked_TruncatedReplyIsAnError(t *testing.T) {
	srv := replyServer(t, "length", `{"verdicts":[{"id":"a-1","outcome":"supp`)
	defer srv.Close()
	c := NewClient(srv.URL, "", "some/model")

	resp, err := c.CompleteChecked(context.Background(), &ChatRequest{
		Messages:  []Message{{Role: "user", Content: "classify"}},
		MaxTokens: 120,
	})
	if !errors.Is(err, ErrReplyTruncated) {
		t.Fatalf("err = %v, want ErrReplyTruncated", err)
	}
	// The cap is in the message, so the log says what to change.
	if !strings.Contains(err.Error(), "max_tokens=120") {
		t.Errorf("error should name the cap in force, got %q", err)
	}
	// The partial reply is still handed back for a caller that wants it.
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		t.Error("the partial reply should come back alongside the error")
	}
}

func TestCompleteChecked_CompleteReplyPassesThrough(t *testing.T) {
	srv := replyServer(t, "stop", `{"verdicts":[]}`)
	defer srv.Close()
	c := NewClient(srv.URL, "", "some/model")

	resp, err := c.CompleteChecked(context.Background(), &ChatRequest{
		Messages:  []Message{{Role: "user", Content: "classify"}},
		MaxTokens: 120,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Choices[0].Message.Content != `{"verdicts":[]}` {
		t.Fatalf("content = %q", resp.Choices[0].Message.Content)
	}
}

// A tool call finishes as "tool_calls", not "stop". That is a complete reply.
func TestCompleteChecked_ToolCallIsNotTruncated(t *testing.T) {
	srv := replyServer(t, "tool_calls", "")
	defer srv.Close()
	c := NewClient(srv.URL, "", "some/model")

	if _, err := c.CompleteChecked(context.Background(), &ChatRequest{
		Messages:  []Message{{Role: "user", Content: "plan"}},
		MaxTokens: 4096,
	}); err != nil {
		t.Fatalf("a tool call is a finished reply, got %v", err)
	}
}

func TestTruncated(t *testing.T) {
	if Truncated(nil) {
		t.Error("nil response is not truncated")
	}
	if Truncated(&ChatResponse{}) {
		t.Error("a response with no choices is not truncated — that is a different fault")
	}
	if Truncated(&ChatResponse{Choices: []Choice{{FinishReason: "stop"}}}) {
		t.Error("stop is not truncated")
	}
	if !Truncated(&ChatResponse{Choices: []Choice{{FinishReason: "length"}}}) {
		t.Error("length is truncated")
	}
	// Any choice being cut is enough.
	if !Truncated(&ChatResponse{Choices: []Choice{{FinishReason: "stop"}, {FinishReason: "length"}}}) {
		t.Error("a truncated second choice must be reported")
	}
}

// A transport error is returned as-is: it is not a truncation, and the
// truncation check must not mask it.
func TestCompleteChecked_TransportErrorIsUnchanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "", "some/model")

	_, err := c.CompleteChecked(context.Background(), &ChatRequest{
		Messages:  []Message{{Role: "user", Content: "x"}},
		MaxTokens: 100,
	})
	if err == nil {
		t.Fatal("want the transport error")
	}
	if errors.Is(err, ErrReplyTruncated) {
		t.Fatalf("a 500 is not a truncation: %v", err)
	}
}
