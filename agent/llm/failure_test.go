package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// The classifier replaces three substring searches, so it is proved against the
// messages those searches matched rather than against its own idea of them.
//
//	llm.IsAuthFailure       eight terms
//	llm.upstreamFailure     four terms and a scan of a hundred status codes
//	agent/scheduler.go      four more of its own
//
// Every string below is one of theirs, in the wording a provider actually uses.

func TestClassifyAgreesWithTheSearchesItReplaces(t *testing.T) {
	for _, c := range []struct {
		msg  string
		want Kind
	}{
		// Credentials. Never retried, so this answer has to win over the status.
		{"HTTP 401: Unauthorized", KindCredentials},
		{"HTTP 403: forbidden", KindCredentials},
		{`{"error":{"code":"invalid_api_key"}} invalid api key`, KindCredentials},
		{"insufficient_quota: You exceeded your current quota", KindCredentials},
		{"402: insufficient credits", KindCredentials},
		{"authentication error", KindCredentials},

		// The other end asking to be left alone.
		{"HTTP 429: Too Many Requests", KindRateLimited},
		{"rate limit exceeded", KindRateLimited},

		// Reached the provider, provider could not serve it.
		{"HTTP 500: internal server error", KindUpstream},
		{"HTTP 502: bad gateway", KindUpstream},
		{"HTTP 503: service unavailable", KindUpstream},
		{"HTTP 529: overloaded", KindUpstream},
		{"provider returned an error with HTTP 200: upstream timed out", KindUpstream},
		{"provider returned no choices with HTTP 200: {}", KindUpstream},
		{"read response: context deadline exceeded", KindUpstream},

		// Nothing this layer acts on.
		{"HTTP 400: bad request", KindNone},
		{"HTTP 404: model not found", KindNone},
		{"parse response: invalid character", KindNone},
		{"", KindNone},
	} {
		if got := Classify(errors.New(c.msg)); got != c.want {
			t.Errorf("%q classified %v, want %v", c.msg, got, c.want)
		}
	}
}

// A typed error answers for itself rather than being read.
func TestATypedFailureIsNotGrepped(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", &CallError{Kind: KindEmpty, Status: 200})
	if got := Classify(err); got != KindEmpty {
		t.Errorf("got %v, want %v — a typed error must not fall through to the text", got, KindEmpty)
	}
	// And it survives errors.Is/As through a wrap.
	var ce *CallError
	if !errors.As(err, &ce) || ce.Kind != KindEmpty {
		t.Error("CallError did not survive being wrapped")
	}
}

// The two endings this package knows about without reading anything.
func TestTheKindsThisPackageKnowsDirectly(t *testing.T) {
	if got := Classify(TruncationError(4096)); got != KindTruncated {
		t.Errorf("a truncated reply classified %v, want %v", got, KindTruncated)
	}
	if got := Classify(context.DeadlineExceeded); got != KindTimeout {
		t.Errorf("our own deadline classified %v, want %v", got, KindTimeout)
	}
	// A caller giving up is not a failure of the call.
	if got := Classify(context.Canceled); got != KindNone {
		t.Errorf("a cancelled call classified %v, want %v", got, KindNone)
	}
	if got := Classify(nil); got != KindNone {
		t.Errorf("nil classified %v", got)
	}
}

// Two endings are worth waiting out, and the provider's own answer wins.
func TestBackoffWaitsOnlyWhereWaitingIsTheAnswer(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"rate limited", &CallError{Kind: KindRateLimited, Status: 429}, true},
		{"service unavailable", &CallError{Kind: KindUpstream, Status: 503}, true},
		{"a plain 500", &CallError{Kind: KindUpstream, Status: 500}, false},
		{"credentials", &CallError{Kind: KindCredentials, Status: 401}, false},
		{"an empty reply", &CallError{Kind: KindEmpty}, false},
		{"from the text", errors.New("HTTP 429: too many requests"), true},
	} {
		if _, got := Backoff(c.err); got != c.want {
			t.Errorf("%s: waiting = %v, want %v", c.name, got, c.want)
		}
	}

	if d, _ := Backoff(&CallError{Kind: KindRateLimited, RetryAfter: 30 * time.Second}); d != 30*time.Second {
		t.Errorf("the provider asked for 30s and got %s", d)
	}
	if d, _ := Backoff(&CallError{Kind: KindRateLimited}); d != defaultBackoff {
		t.Errorf("a provider that did not say got %s, want the %s default", d, defaultBackoff)
	}
}

// Retry-After comes in two spellings and an unreadable one is no value rather
// than an error.
func TestRetryAfterIsReadInBothSpellings(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "12")
	if got := retryAfterHeader(h); got != 12*time.Second {
		t.Errorf("seconds: got %s, want 12s", got)
	}
	h.Set("Retry-After", time.Now().Add(20*time.Second).UTC().Format(http.TimeFormat))
	if got := retryAfterHeader(h); got < 15*time.Second || got > 21*time.Second {
		t.Errorf("date: got %s, want about 20s", got)
	}
	h.Set("Retry-After", "soon")
	if got := retryAfterHeader(h); got != 0 {
		t.Errorf("an unreadable value gave %s, want none", got)
	}
	if got := retryAfterHeader(http.Header{}); got != 0 {
		t.Errorf("an absent header gave %s, want none", got)
	}
}

// The sentence is unchanged by any of this. It is what an operator reads in a
// log, and what an error arriving from outside this package is still matched
// on — so the kind travels beside the text rather than rewriting it.
func TestTheMessageIsUnchanged(t *testing.T) {
	e := &CallError{Kind: KindRateLimited, Status: 429, Err: errors.New("HTTP 429: Too Many Requests")}
	if got := e.Error(); got != "HTTP 429: Too Many Requests" {
		t.Errorf("message = %q, want the sentence the call would have produced anyway", got)
	}
	// And a caller still grepping it gets the same answer it always did.
	if Classify(errors.New(e.Error())) != KindRateLimited {
		t.Error("the sentence no longer classifies the way it used to")
	}
}
