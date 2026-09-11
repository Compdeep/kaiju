package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Every ending, and what asking again is worth for it.
//
// The property is not "it retries" — it is that the second attempt asks a
// DIFFERENT question wherever the first question was the problem, and the same
// one only where nothing about it was wrong. A retry that sends the same
// request to the same fault is the first attempt a second time.

// facts is the ordinary model of 2026: thinks by default, and whether it can be
// told not to is the thing the remedy turns on.
func facts(optional bool, maxOut int) Catalog {
	return func(string) (ModelFacts, bool) {
		return ModelFacts{
			MaxOutputTokens: maxOut,
			Thinking:        Thinking{Default: true, Optional: optional},
		}, true
	}
}

func TestEachEndingGetsTheRemedyItDeserves(t *testing.T) {
	prose := func() *ChatRequest {
		return &ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}, MaxTokens: 4096}
	}
	empty := &ChatResponse{Choices: []Choice{{FinishReason: "length"}}}

	for _, c := range []struct {
		name     string
		catalog  Catalog
		resp     *ChatResponse
		err      error
		wantOK   bool
		wantSame bool // the same request, unchanged
		wantWait bool
	}{
		{name: "bad credentials are never retried",
			err: &CallError{Kind: KindCredentials, Status: 401}},
		{name: "a reply cut short is an answer, not a failure",
			err: TruncationError(4096)},
		{name: "a good reply is left alone",
			resp: &ChatResponse{Choices: []Choice{{Message: Message{Content: "hello"}}}}},

		{name: "transport is the same request again",
			err: &CallError{Kind: KindTransport}, wantOK: true, wantSame: true},
		{name: "an upstream failure is the same request again",
			err: &CallError{Kind: KindUpstream, Status: 500}, wantOK: true, wantSame: true},
		{name: "a rate limit waits first",
			err:    &CallError{Kind: KindRateLimited, Status: 429, RetryAfter: time.Second},
			wantOK: true, wantSame: true, wantWait: true},

		{name: "an empty reply asks differently",
			catalog: facts(true, 0), resp: empty, wantOK: true},
		{name: "our own deadline asks differently",
			catalog: facts(true, 0), err: context.DeadlineExceeded, wantOK: true},
		{name: "an empty reply from a model nobody knows is left alone",
			resp: empty},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl := NewClient("http://example.invalid", "", "m")
			if c.catalog != nil {
				cl.Catalog(c.catalog)
			}
			req := prose()
			got, wait, ok := cl.recoverable(req, c.resp, c.err)
			if ok != c.wantOK {
				t.Fatalf("retry = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if same := got == req; same != c.wantSame {
				t.Errorf("same request = %v, want %v", same, c.wantSame)
			}
			if (wait > 0) != c.wantWait {
				t.Errorf("wait = %s, want any = %v", wait, c.wantWait)
			}
		})
	}
}

// A model that can be asked to stop is asked to stop, and carries its own
// thinking into the second attempt rather than starting from nothing.
func TestTheSecondAttemptStopsTheThinkingAndKeepsIt(t *testing.T) {
	c := NewClient("http://example.invalid", "", "m").Catalog(facts(true, 0))
	req := &ChatRequest{Messages: []Message{{Role: "user", Content: "which invoice?"}}, MaxTokens: 4096}
	cut := &ChatResponse{Choices: []Choice{{
		FinishReason: "length",
		Message:      Message{Reasoning: "they mean the March one"},
	}}}

	got, _, ok := c.recoverable(req, cut, nil)
	if !ok {
		t.Fatal("an empty reply was not re-asked")
	}
	if !got.Think.Off() {
		t.Errorf("the second attempt asked for %+v, want thinking off", got.Think)
	}
	if got.MaxTokens != req.MaxTokens {
		t.Errorf("the cap moved to %d; a model that can stop needs room for the answer, not more room", got.MaxTokens)
	}
	last := got.Messages[len(got.Messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "they mean the March one") {
		t.Errorf("the first attempt's thinking was not handed on:\n%s", last.Content)
	}
	if len(req.Messages) != 1 {
		t.Error("the original request was modified; a retry that mutates what it retries cannot be run twice")
	}
}

// A model that cannot be asked to stop is given room instead — telling it to
// stop is the same call again.
func TestALockedModelIsGivenRoomRatherThanAnInstruction(t *testing.T) {
	c := NewClient("http://example.invalid", "", "m").Catalog(facts(false, 65536))
	req := &ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}, MaxTokens: 4096}

	got, _, ok := c.recoverable(req, &ChatResponse{Choices: []Choice{{FinishReason: "length"}}}, nil)
	if !ok {
		t.Fatal("no second attempt was offered")
	}
	if got.Think != nil && got.Think.Off() {
		t.Error("a model that cannot stop reasoning was told to stop anyway")
	}
	if got.MaxTokens <= req.MaxTokens {
		t.Errorf("the cap stayed at %d; a locked model needs room to finish", got.MaxTokens)
	}
	if got.MaxTokens != raisedCapFloor {
		t.Errorf("the raised cap is %d, want %d", got.MaxTokens, raisedCapFloor)
	}
}

// And where there is no more room, there is no second question to ask.
func TestALockedModelWithNothingLeftIsNotRetried(t *testing.T) {
	c := NewClient("http://example.invalid", "", "m").Catalog(facts(false, 4096))
	req := &ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}, MaxTokens: 4096}
	if _, _, ok := c.recoverable(req, &ChatResponse{Choices: []Choice{{FinishReason: "length"}}}, nil); ok {
		t.Error("a retry was offered with nothing to change about it")
	}
}

// Exactly one. A third attempt under bounds that have not changed is the second
// one again.
func TestOnlyOneRetryEverHappens(t *testing.T) {
	freshBreaker(t)
	var sent atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sent.Add(1)
		http.Error(w, "still broken", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "m")
	if _, err := c.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "x"}}}); err == nil {
		t.Fatal("want an error after both attempts failed")
	}
	if n := sent.Load(); n != 2 {
		t.Errorf("%d requests were sent, want 2", n)
	}
}

// A retry that fixes it is invisible to the caller, which is the point.
func TestARetryThatSucceedsJustAnswers(t *testing.T) {
	freshBreaker(t)
	var sent atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if sent.Add(1) == 1 {
			http.Error(w, "blip", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"the answer"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "m")
	resp, err := c.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatalf("a blip was not recovered: %v", err)
	}
	if resp.Choices[0].Message.Content != "the answer" {
		t.Errorf("content = %q", resp.Choices[0].Message.Content)
	}
	if n := sent.Load(); n != 2 {
		t.Errorf("%d requests were sent, want 2", n)
	}
}

// Bad credentials must reach the caller on the first attempt: a run that
// retries an invalid key spends its budget failing identically.
func TestCredentialsAreNotRetriedOnTheWire(t *testing.T) {
	freshBreaker(t)
	var sent atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sent.Add(1)
		http.Error(w, "invalid api key", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", "m")
	_, err := c.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "x"}}})
	if err == nil {
		t.Fatal("want an error")
	}
	if Classify(err) != KindCredentials {
		t.Errorf("classified %v, want %v", Classify(err), KindCredentials)
	}
	if n := sent.Load(); n != 1 {
		t.Errorf("%d requests were sent, want 1 — a key does not get better on the second try", n)
	}
}

// A caller that has given up is not held through a backoff.
func TestABackoffDoesNotOutliveTheCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitBefore(ctx, time.Hour) {
		t.Error("the wait completed on a cancelled context")
	}
	if !waitBefore(context.Background(), 0) {
		t.Error("a zero wait did not return at once")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
