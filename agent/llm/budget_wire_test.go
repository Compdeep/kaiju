package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// bodyCapture serves one ordinary reply and keeps the request body it was sent,
// so a test can assert what actually went on the wire rather than what a
// resolver returned on the way there.
func bodyCapture(t *testing.T, got *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("the request body did not decode: %v", err)
		}
		*got = body
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
}

// reasoningOnTheWire pulls the reasoning object out of a captured body.
func reasoningOnTheWire(t *testing.T, body map[string]any) (map[string]any, bool) {
	t.Helper()
	r, ok := body["reasoning"]
	if !ok {
		return nil, false
	}
	m, ok := r.(map[string]any)
	if !ok {
		t.Fatalf("reasoning is %T on the wire, not an object", r)
	}
	return m, true
}

// A measured model is sent the bound, with the number the caller chose.
//
// Everything between the catalog flag and the wire is covered elsewhere one
// hop at a time. This is the whole walk in one assertion, against the body a
// server receives, because a bound that resolves correctly and then fails to
// serialise is indistinguishable from one the host ignored.
func TestAThinkingBudgetReachesTheWire(t *testing.T) {
	var body map[string]any
	srv := bodyCapture(t, &body)
	defer srv.Close()

	c := NewClient(srv.URL, "k", "measured-model").Catalog(func(string) (ModelFacts, bool) {
		return ModelFacts{Thinking: Thinking{Default: true, Optional: true, Budget: true}}, true
	})

	_, err := c.Complete(context.Background(), &ChatRequest{
		Messages:  []Message{{Role: "user", Content: "hello"}},
		MaxTokens: 8192,
		Think:     &Reasoning{Want: WantOn, Budget: 5120},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	r, ok := reasoningOnTheWire(t, body)
	if !ok {
		t.Fatal("no reasoning object was sent — the bound never left the process")
	}
	if got, want := r["max_tokens"], float64(5120); got != want {
		t.Errorf("reasoning.max_tokens = %v, want %v", got, want)
	}
	if enabled, ok := r["enabled"].(bool); !ok || !enabled {
		t.Errorf("reasoning.enabled = %v, want true", r["enabled"])
	}
	// The bound is on the thinking, not on the reply: the answer keeps the rest.
	if got, want := body["max_tokens"], float64(8192); got != want {
		t.Errorf("max_tokens = %v, want %v — the reply allowance must not shrink to the bound", got, want)
	}
}

// An unmeasured model is sent no bound, however loudly the caller asks.
//
// This is the half that keeps the catalog honest. A budget sent to a host that
// drops it changes nothing and says nothing, so it is withheld until somebody
// has measured that it is honoured.
func TestAnUnmeasuredModelIsSentNoBudget(t *testing.T) {
	var body map[string]any
	srv := bodyCapture(t, &body)
	defer srv.Close()

	c := NewClient(srv.URL, "k", "unmeasured-model").Catalog(func(string) (ModelFacts, bool) {
		return ModelFacts{Thinking: Thinking{Default: true, Optional: true}}, true
	})

	_, err := c.Complete(context.Background(), &ChatRequest{
		Messages:  []Message{{Role: "user", Content: "hello"}},
		MaxTokens: 8192,
		Think:     &Reasoning{Want: WantOn, Budget: 5120},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if r, ok := reasoningOnTheWire(t, body); ok {
		if _, had := r["max_tokens"]; had {
			t.Errorf("a bound was sent to a model nobody measured: %v", r)
		}
	}
}

// Thinking off reaches the wire as off, which is what the reframe edges rely
// on. Measured on 177 live calls before this test existed; asserted here so a
// change to the resolver cannot quietly take it away.
func TestThinkingOffReachesTheWire(t *testing.T) {
	var body map[string]any
	srv := bodyCapture(t, &body)
	defer srv.Close()

	c := NewClient(srv.URL, "k", "optional-model").Catalog(func(string) (ModelFacts, bool) {
		return ModelFacts{Thinking: Thinking{Default: true, Optional: true}}, true
	})

	_, err := c.Complete(context.Background(), WithoutReasoning(&ChatRequest{
		Messages:  []Message{{Role: "user", Content: "hello"}},
		MaxTokens: 2000,
	}))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	r, ok := reasoningOnTheWire(t, body)
	if !ok {
		t.Fatal("no reasoning object was sent — thinking was left to the provider's default, which is on")
	}
	if enabled, isBool := r["enabled"].(bool); !isBool || enabled {
		t.Errorf("reasoning.enabled = %v, want false", r["enabled"])
	}
}
