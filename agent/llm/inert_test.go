package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An application that tells this package nothing about its models gets exactly
// the request it wrote.
//
// This is the contract every behaviour keyed on ModelFacts rests on, and the
// one that cannot be checked from inside this repo by any other means: the
// embedding application builds its own clients with llm.NewClient and no
// catalog at all, and calls Complete directly in six places. A behaviour that
// leaks into those calls changes a product nobody here can run.
//
// So it is asserted on the BODY, not on the response: what went on the wire is
// the only thing the far end sees.

// bodyOf sends one request through a client and returns the raw JSON body.
func bodyOf(t *testing.T, build func(url string) *Client, req *ChatRequest) map[string]any {
	t.Helper()
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw = readAll(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	if _, err := build(srv.URL).Complete(context.Background(), req); err != nil {
		t.Fatalf("complete: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("the body was not JSON: %v\n%s", err, raw)
	}
	return body
}

func readAll(t *testing.T, r *http.Request) []byte {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// The shapes that matter: prose, a forced single-tool call inside a tiny budget,
// and a request that already says something about reasoning.
func inertShapes() map[string]*ChatRequest {
	return map[string]*ChatRequest{
		"prose": {
			Messages:  []Message{{Role: "user", Content: "hello"}},
			MaxTokens: 8192,
		},
		"a forced call in a small budget": {
			Messages:   []Message{{Role: "system", Content: "route"}, {Role: "user", Content: "hi"}},
			Tools:      []ToolDef{{Type: "function", Function: FunctionDef{Name: "route", Parameters: json.RawMessage(`{"type":"object"}`)}}},
			ToolChoice: "required",
			MaxTokens:  128,
		},
		"a large forced call": {
			Messages:   []Message{{Role: "system", Content: "plan"}, {Role: "user", Content: "do the thing"}},
			Tools:      []ToolDef{{Type: "function", Function: FunctionDef{Name: "plan", Parameters: json.RawMessage(`{"type":"object"}`)}}},
			ToolChoice: "required",
			MaxTokens:  8192,
		},
	}
}

func TestNoCatalogLeavesTheRequestAlone(t *testing.T) {
	for name, req := range inertShapes() {
		t.Run(name, func(t *testing.T) {
			want := req.MaxTokens
			body := bodyOf(t, func(url string) *Client {
				return NewClient(url, "", "some/model")
			}, req)

			if got, ok := body["max_tokens"].(float64); !ok || int(got) != want {
				t.Errorf("max_tokens on the wire = %v, want the %d the caller asked for", body["max_tokens"], want)
			}
			for _, k := range []string{"reasoning", "thinking", "reasoning_effort", "thinking_budget"} {
				if v, present := body[k]; present {
					t.Errorf("%q reached the wire as %v; a client told nothing about its models "+
						"must ask for nothing", k, v)
				}
			}
		})
	}
}

// The older narrow seam still does what it did and nothing more: Limits sizes
// the reply, and says nothing about thinking.
func TestLimitsAloneStillOnlySizesTheReply(t *testing.T) {
	req := &ChatRequest{
		Messages:  []Message{{Role: "user", Content: "hello"}},
		MaxTokens: 8192,
	}
	body := bodyOf(t, func(url string) *Client {
		return NewClient(url, "", "some/model").
			Limits(func(string) (int, int) { return 128000, 700 })
	}, req)

	if got, _ := body["max_tokens"].(float64); int(got) != 700 {
		t.Errorf("max_tokens = %v, want 700 — the model's published maximum reply", body["max_tokens"])
	}
	if _, present := body["reasoning"]; present {
		t.Error("Limits alone asked for reasoning; it answers one question and must not answer others")
	}
}

// A catalog that does not carry the model is the same as no catalog. This is
// the ordinary case for a self-hosted endpoint, not an error.
func TestACatalogThatDoesNotCarryTheModelChangesNothing(t *testing.T) {
	req := &ChatRequest{
		Messages:  []Message{{Role: "user", Content: "hello"}},
		MaxTokens: 4096,
	}
	body := bodyOf(t, func(url string) *Client {
		return NewClient(url, "", "selfhosted/something").
			Catalog(func(string) (ModelFacts, bool) { return ModelFacts{}, false })
	}, req)

	if got, _ := body["max_tokens"].(float64); int(got) != 4096 {
		t.Errorf("max_tokens = %v, want the 4096 the caller asked for", body["max_tokens"])
	}
	if _, present := body["reasoning"]; present {
		t.Error("an unknown model was sent a reasoning instruction")
	}
}

// facts reports what it was told, and admits when it was told nothing.
func TestFactsFallsBackToTheNarrowLookups(t *testing.T) {
	plain := NewClient("http://example.invalid", "", "m")
	if _, ok := plain.facts("m"); ok {
		t.Error("a client told nothing claims to know something")
	}

	sized := NewClient("http://example.invalid", "", "m").
		Limits(func(string) (int, int) { return 1000, 100 })
	f, ok := sized.facts("m")
	if !ok || f.ContextTokens != 1000 || f.MaxOutputTokens != 100 {
		t.Errorf("Limits did not reach facts: %+v ok=%v", f, ok)
	}
	if f.Thinking.Default {
		t.Error("Limits answered a question it was not asked")
	}

	full := NewClient("http://example.invalid", "", "m").
		Catalog(func(string) (ModelFacts, bool) {
			return ModelFacts{ContextTokens: 2, Thinking: Thinking{Default: true, Optional: true}}, true
		}).
		Limits(func(string) (int, int) { return 1000, 100 })
	if f, _ := full.facts("m"); f.ContextTokens != 2 || !f.Thinking.Optional {
		t.Errorf("the catalog did not win over the narrow lookups: %+v", f)
	}
}
