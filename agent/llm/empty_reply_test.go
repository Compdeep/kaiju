package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A gateway answers 200 with an error object and no choices when the upstream
// provider fails, rate-limits or drops a request. The body was decoded into a
// struct that had no field for it, so the caller saw a reply with no choices and
// no reason, and the trace recorded an empty response with no error against it.
// Measured at 6.9% of one deployment's
// planning calls, each abandoning a run.
func TestAnErrorInA200IsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":{"message":"Provider returned error","code":429},"choices":[]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k", "m")
	_, err := c.Complete(context.Background(), &ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("a 200 carrying an error was reported as a successful empty reply")
	}
	if !strings.Contains(err.Error(), "Provider returned error") {
		t.Errorf("the provider's own message is missing from %q", err)
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("the provider's code is missing from %q", err)
	}
}

// No choices and nothing saying why: the body is the only evidence there is, so
// a bounded amount of it travels with the error.
func TestNoChoicesCarriesTheBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"gen-1","choices":[],"usage":{"total_tokens":0},"provider":"upstream-x"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k", "m")
	_, err := c.Complete(context.Background(), &ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("an empty reply was reported as a success")
	}
	if !strings.Contains(err.Error(), "upstream-x") {
		t.Errorf("the body is not in the error, so the reason is unrecoverable: %q", err)
	}
}

// A reply that arrived is untouched by any of this.
func TestAGoodReplyIsUnaffected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k", "m")
	resp, err := c.Complete(context.Background(), &ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("a good reply failed: %v", err)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "ok" {
		t.Errorf("reply mangled: %+v", resp.Choices)
	}
}
