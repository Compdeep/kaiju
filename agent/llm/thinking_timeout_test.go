package llm

import (
	"testing"
	"time"
)

// A thinking model waits twice as long, because the hidden tokens are real
// generation the caller sits through. Measured on one planner prompt: 75s, 79s
// and 206s for a thinking model against 54s to 62s for one that does not think,
// with 206s close enough to a 300s ceiling that a larger prompt goes over.
func TestAThinkingModelGetsTheLongerDeadline(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", "plain").
		Thinks(func(m string) bool { return m == "thinker" })

	if got := c.timeoutFor(&ChatRequest{Model: "thinker"}); got != thinkingRequestTimeout {
		t.Errorf("thinking model deadline = %s, want %s", got, thinkingRequestTimeout)
	}
	if got := c.timeoutFor(&ChatRequest{Model: "plain"}); got != requestTimeout {
		t.Errorf("non-thinking deadline = %s, want %s", got, requestTimeout)
	}
}

// An unnamed model on the request is the client's own, which is the order
// Complete resolves them in.
func TestTheClientsOwnModelDecidesWhenTheRequestNamesNone(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", "thinker").
		Thinks(func(m string) bool { return m == "thinker" })
	if got := c.timeoutFor(&ChatRequest{}); got != thinkingRequestTimeout {
		t.Errorf("deadline = %s, want the long one — the client's model thinks", got)
	}
}

// No catalog, or a model it does not carry: the ordinary deadline. An unknown
// model waits the same as one that does not think, which is the safe direction —
// a stuck provider should still be abandoned at the shorter clock.
func TestAnUnknownModelGetsTheOrdinaryDeadline(t *testing.T) {
	none := NewClient("http://127.0.0.1:1", "", "whatever")
	if got := none.timeoutFor(&ChatRequest{Model: "whatever"}); got != requestTimeout {
		t.Errorf("deadline without a catalog = %s, want %s", got, requestTimeout)
	}
	known := NewClient("http://127.0.0.1:1", "", "x").
		Thinks(func(m string) bool { return m == "thinker" })
	if got := known.timeoutFor(&ChatRequest{Model: "never-heard-of-it"}); got != requestTimeout {
		t.Errorf("deadline for an unknown model = %s, want %s", got, requestTimeout)
	}
}

// The client's hard ceiling has to cover the longest per-request deadline, or a
// connection is cut before the deadline it was given can expire.
func TestTheHTTPCeilingCoversTheLongestDeadline(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", "x")
	if c.http.Timeout < thinkingRequestTimeout {
		t.Errorf("http ceiling %s is below the thinking deadline %s", c.http.Timeout, thinkingRequestTimeout)
	}
	if requestTimeout >= thinkingRequestTimeout {
		t.Errorf("the thinking deadline (%s) must exceed the ordinary one (%s)", thinkingRequestTimeout, requestTimeout)
	}
	_ = time.Second
}
