package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// A model measured slow waits longer, on top of whichever deadline it was
// already getting.
//
// The two clocks read the same measurement — this one and the engine's round
// deadline — because widening one alone means the run is cut by the other and
// the error names the wrong clock.
func TestASlowModelWaitsLonger(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", "plain").
		Thinks(func(m string) bool { return m == "thinker" }).
		Pace(func(m string) float64 {
			if m == "thinker" || m == "plain" {
				return 1.5
			}
			return 1
		})

	if got := c.timeoutFor(&ChatRequest{Model: "plain"}); got != requestTimeout*3/2 {
		t.Errorf("slow non-thinking model = %s, want half again %s", got, requestTimeout)
	}
	if got := c.timeoutFor(&ChatRequest{Model: "thinker"}); got != thinkingRequestTimeout*3/2 {
		t.Errorf("slow thinking model = %s, want half again %s", got, thinkingRequestTimeout)
	}
	if got := c.timeoutFor(&ChatRequest{Model: "brisk"}); got != requestTimeout {
		t.Errorf("ordinary model = %s, want the ordinary %s", got, requestTimeout)
	}
}

// The pace only ever adds. A lookup answering below 1 changes nothing: a
// deadline shortened by a measurement is a run cut off by an average.
func TestAPaceNeverShortensADeadline(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", "quick").Pace(func(string) float64 { return 0.1 })
	if got := c.timeoutFor(&ChatRequest{Model: "quick"}); got != requestTimeout {
		t.Errorf("deadline = %s, want the ordinary %s", got, requestTimeout)
	}
}

// The connection ceiling has to sit above the longest deadline a pace can
// produce, or the scaling is undone by the thing meant to be a backstop.
func TestTheConnectionCeilingClearsTheLongestDeadline(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "", "thinker").
		Thinks(func(string) bool { return true }).
		Pace(func(string) float64 { return 2 })

	longest := c.timeoutFor(&ChatRequest{Model: "thinker"})
	if connectionCeiling < longest {
		t.Errorf("the ceiling is %s and the deadline can reach %s, so the ceiling cuts first",
			connectionCeiling, longest)
	}
}

// A request that was streamed once must not ask to be streamed again.
//
// completeStreamResp sets Stream on the CALLER'S request, which outlives the
// call, so every retry built from a streamed request asked the provider to
// stream and then read the reply as one document: "parse response: invalid
// character 'd' looking for beginning of value" — the 'd' beginning "data:".
// That is every recovery after a streamed call, on three lanes.
func TestANonStreamedSendClearsTheStreamingFlag(t *testing.T) {
	var asked struct {
		stream bool
		opts   bool
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream        bool `json:"stream"`
			StreamOptions *struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		asked.stream, asked.opts = body.Stream, body.StreamOptions != nil
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k", "m")
	// As a caller's request looks after it has been streamed once.
	req := &ChatRequest{
		Messages:      []Message{{Role: "user", Content: "again"}},
		Stream:        true,
		StreamOptions: &StreamOptions{IncludeUsage: true},
	}
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if asked.stream || asked.opts {
		t.Errorf("the provider was asked to stream on a non-streamed send (stream=%v, options=%v)",
			asked.stream, asked.opts)
	}
	if req.Stream {
		t.Error("the request still says stream, so the next caller to read it is misled")
	}
}
