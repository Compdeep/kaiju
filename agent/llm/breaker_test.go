package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func freshBreaker(t *testing.T) {
	t.Helper()
	providerBreaker.mu.Lock()
	providerBreaker.consecutive, providerBreaker.open, providerBreaker.lastReason = 0, false, ""
	providerBreaker.mu.Unlock()
	t.Cleanup(func() {
		providerBreaker.mu.Lock()
		providerBreaker.consecutive, providerBreaker.open, providerBreaker.lastReason = 0, false, ""
		providerBreaker.mu.Unlock()
	})
}

// A provider answering 200 with an error is billed for whatever the model
// generated before it was abandoned, returns nothing, and the caller's retry
// sends the same work again. Measured on one deployment: 46 of 57 calls in
// three hours, about 40% of 1.15 million tokens paid for and thrown away.
func TestTheBreakerOpensAfterTenUpstreamFailures(t *testing.T) {
	freshBreaker(t)
	var sent int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sent++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"error":{"message":"The operation was aborted","code":504},"choices":[]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k", "m")
	req := &ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}
	for i := 0; i < breakerThreshold; i++ {
		if _, err := c.Complete(context.Background(), req); err == nil {
			t.Fatalf("call %d: want an error", i)
		}
	}
	if sent != breakerThreshold {
		t.Fatalf("%d requests reached the provider, want %d", sent, breakerThreshold)
	}
	// The next one is refused without going to the wire — nothing sent, nothing
	// billed, and the caller can tell it apart from a failure of its own.
	_, err := c.Complete(context.Background(), req)
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("err = %v, want ErrProviderUnavailable", err)
	}
	if sent != breakerThreshold {
		t.Errorf("a request was sent while the breaker was open: %d", sent)
	}
	if !strings.Contains(err.Error(), "aborted") && !strings.Contains(err.Error(), "200 with no reply") {
		t.Errorf("the error does not say why: %v", err)
	}
}

// One answer says the provider is answering. Nine failures then a reply must
// leave the breaker closed, or an intermittent provider trips it on a run of
// bad luck.
func TestOneReplyResetsTheCount(t *testing.T) {
	freshBreaker(t)
	var fail bool = true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if fail {
			_, _ = w.Write([]byte(`{"error":{"message":"aborted","code":504},"choices":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k", "m")
	req := &ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}
	for i := 0; i < breakerThreshold-1; i++ {
		_, _ = c.Complete(context.Background(), req)
	}
	fail = false
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatalf("a good reply failed: %v", err)
	}
	fail = true
	// The count started again, so one more failure must not open it.
	if _, err := c.Complete(context.Background(), req); errors.Is(err, ErrProviderUnavailable) {
		t.Error("the breaker opened without a fresh run of failures")
	}
}

// The distinction the whole thing rests on: a reply that arrived and was not
// what we wanted is the model's answer, not the provider failing. Counting
// those would let a model having a bad run stop every caller from asking.
func TestAModelsOwnFailuresDoNotOpenIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"a truncated reply", TruncationError(256)},
		{"our own cancellation", context.Canceled},
		{"a parse failure", errors.New("parse response: invalid character 'x'")},
	} {
		if upstream, _ := upstreamFailure(tc.err); upstream {
			t.Errorf("%s was counted against the provider", tc.name)
		}
	}
}

// And the ones that are the provider's.
func TestUpstreamFailuresAreRecognised(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"200 with an error body", errors.New("provider returned an error with HTTP 200: aborted (code 504)")},
		{"200 with no choices", errors.New("provider returned no choices with HTTP 200: {}")},
		{"a timeout", context.DeadlineExceeded},
		{"a read timeout", errors.New("read response: context deadline exceeded")},
		{"rate limiting", fmt.Errorf("HTTP %d: slow down", http.StatusTooManyRequests)},
		{"a gateway timeout", fmt.Errorf("HTTP %d: upstream gone", http.StatusGatewayTimeout)},
	} {
		if upstream, reason := upstreamFailure(tc.err); !upstream {
			t.Errorf("%s was not counted against the provider", tc.name)
		} else if reason == "" {
			t.Errorf("%s gave no reason", tc.name)
		}
	}
}

// A breaker with no way back would stop the product until a restart. After the
// cooldown exactly one request is allowed, and its outcome decides.
func TestTheCooldownLetsOneRequestDecide(t *testing.T) {
	freshBreaker(t)
	providerBreaker.mu.Lock()
	providerBreaker.open, providerBreaker.consecutive = true, breakerThreshold
	providerBreaker.openedAt = time.Now().Add(-breakerCooldown - time.Second)
	providerBreaker.mu.Unlock()

	if err := providerBreaker.allow(); err != nil {
		t.Fatalf("the cooldown did not let a request through: %v", err)
	}
	// One failure returns it to open, rather than nine more.
	providerBreaker.failed("still down")
	if err := providerBreaker.allow(); !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("a failure after the cooldown did not reopen it: %v", err)
	}
}
