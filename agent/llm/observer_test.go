package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// capture installs an observer for the duration of the test and returns a
// pointer to what it saw. The observer is process-wide, so it must be removed
// again or it leaks into other tests.
func capture(t *testing.T) *struct {
	mu    sync.Mutex
	calls int
	req   *ChatRequest
	resp  *ChatResponse
	err   error
} {
	t.Helper()
	got := &struct {
		mu    sync.Mutex
		calls int
		req   *ChatRequest
		resp  *ChatResponse
		err   error
	}{}
	SetCallObserver(func(_ context.Context, req *ChatRequest, resp *ChatResponse, err error) {
		got.mu.Lock()
		defer got.mu.Unlock()
		got.calls++
		got.req, got.resp, got.err = req, resp, err
	})
	t.Cleanup(func() { SetCallObserver(nil) })
	return got
}

func okServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(ChatResponse{
			ID:      "resp-1",
			Choices: []Choice{{Message: Message{Role: "assistant", Content: "hello"}}},
			Usage:   Usage{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10},
		})
	}))
}

func TestCallObserverFiresOnSuccess(t *testing.T) {
	srv := okServer(t)
	defer srv.Close()
	got := capture(t)

	c := NewClient(srv.URL, "k", "m")
	if _, err := c.Complete(context.Background(), &ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	got.mu.Lock()
	defer got.mu.Unlock()
	if got.calls != 1 {
		t.Fatalf("observer fired %d times, want 1", got.calls)
	}
	if got.err != nil {
		t.Errorf("err = %v, want nil", got.err)
	}
	if got.req == nil || len(got.req.Messages) != 1 || got.req.Messages[0].Content != "hi" {
		t.Errorf("observer did not receive the request as sent: %+v", got.req)
	}
	if got.resp == nil || len(got.resp.Choices) == 0 || got.resp.Choices[0].Message.Content != "hello" {
		t.Errorf("observer did not receive the response: %+v", got.resp)
	}
	if got.resp != nil && got.resp.Usage.TotalTokens != 10 {
		t.Errorf("tokens = %d, want 10", got.resp.Usage.TotalTokens)
	}
}

// A failed call is usually the one worth logging, so the observer must see it.
func TestCallObserverFiresOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream exploded", http.StatusInternalServerError)
	}))
	defer srv.Close()
	got := capture(t)

	c := NewClient(srv.URL, "k", "m")
	if _, err := c.Complete(context.Background(), &ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}); err == nil {
		t.Fatal("expected an error from a 500")
	}

	got.mu.Lock()
	defer got.mu.Unlock()
	if got.calls != 1 {
		t.Fatalf("observer fired %d times, want 1", got.calls)
	}
	if got.err == nil {
		t.Error("observer received a nil error for a failed call")
	}
}

// The API key must never reach an observer — it lives on the Client and travels
// as a header, not in the request struct.
func TestCallObserverNeverSeesTheAPIKey(t *testing.T) {
	srv := okServer(t)
	defer srv.Close()
	got := capture(t)

	c := NewClient(srv.URL, "sk-super-secret", "m")
	if _, err := c.Complete(context.Background(), &ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	got.mu.Lock()
	defer got.mu.Unlock()
	blob, _ := json.Marshal(got.req)
	if len(blob) > 0 && containsSecret(string(blob)) {
		t.Fatalf("API key leaked into the observed request: %s", blob)
	}
}

func containsSecret(s string) bool {
	for i := 0; i+len("sk-super-secret") <= len(s); i++ {
		if s[i:i+len("sk-super-secret")] == "sk-super-secret" {
			return true
		}
	}
	return false
}

func TestNoObserverIsSafe(t *testing.T) {
	SetCallObserver(nil)
	srv := okServer(t)
	defer srv.Close()

	c := NewClient(srv.URL, "k", "m")
	if _, err := c.Complete(context.Background(), &ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Complete with no observer: %v", err)
	}
}

func TestAttributionDefaultsAndOverride(t *testing.T) {
	t.Cleanup(func() {
		attrMu.Lock()
		attrReferer, attrTitle = defaultReferer, defaultTitle
		attrMu.Unlock()
	})

	var gotReferer, gotTitle string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("HTTP-Referer")
		gotTitle = r.Header.Get("X-Title")
		_ = json.NewEncoder(w).Encode(ChatResponse{Choices: []Choice{{Message: Message{Content: "ok"}}}})
	}))
	defer srv.Close()

	call := func() {
		c := NewClientWithProvider(ProviderOpenRouter, srv.URL, "k", "m")
		if _, err := c.Complete(context.Background(), &ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	}

	call()
	if gotTitle != defaultTitle || gotReferer != defaultReferer {
		t.Errorf("defaults not sent: referer=%q title=%q", gotReferer, gotTitle)
	}

	SetAttribution("https://example.invalid/app", "ExampleApp")
	call()
	if gotTitle != "ExampleApp" || gotReferer != "https://example.invalid/app" {
		t.Errorf("override not sent: referer=%q title=%q", gotReferer, gotTitle)
	}

	// An empty value must not blank a header — it leaves the current value.
	SetAttribution("", "")
	call()
	if gotTitle != "ExampleApp" {
		t.Errorf("empty override clobbered the title: %q", gotTitle)
	}
}
