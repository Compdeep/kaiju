package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// bodyOf runs one call against a server that keeps the request body.
func bodyOf(t *testing.T, build func(url string) *Client, req *ChatRequest) map[string]any {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	if _, err := build(srv.URL).Complete(context.Background(), req); err != nil {
		t.Fatalf("call failed: %v", err)
	}
	return body
}

// The catalog's parameters reach the wire as written.
//
// They are free-form because the controls for reasoning are not one vocabulary:
// thinking_budget, enable_thinking and budget_tokens are the same idea at three
// providers, and a field here for each is a release every time one is added.
func TestAModelsParametersAreSentAsWritten(t *testing.T) {
	body := bodyOf(t,
		func(url string) *Client {
			return NewClient(url, "k", "kimi").Parameters(func(string) map[string]any {
				return map[string]any{"thinking_budget": 6144, "enable_thinking": true}
			})
		},
		&ChatRequest{Messages: []Message{{Role: "user", Content: "go"}}},
	)

	if got := body["thinking_budget"]; got != float64(6144) {
		t.Errorf("thinking_budget = %v, want 6144", got)
	}
	if got := body["enable_thinking"]; got != true {
		t.Errorf("enable_thinking = %v, want true", got)
	}
	// And the ordinary fields still went.
	if body["model"] != "kimi" {
		t.Errorf("model = %v, want the client's own", body["model"])
	}
}

// What the engine set for this call beats the same key from a file.
//
// The lanes that force a small call switch thinking off on every one of them,
// measured, and a catalog entry must not be able to switch it back on.
func TestTheCallsOwnFieldsBeatTheCatalogs(t *testing.T) {
	req := &ChatRequest{Messages: []Message{{Role: "user", Content: "go"}}}
	WithoutReasoning(req)

	body := bodyOf(t,
		func(url string) *Client {
			return NewClient(url, "k", "kimi").Parameters(func(string) map[string]any {
				return map[string]any{"reasoning": map[string]any{"enabled": true}}
			})
		}, req)

	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning = %v, want the object the call set", body["reasoning"])
	}
	if reasoning["enabled"] != false {
		t.Errorf("reasoning.enabled = %v — the file overrode the lane's own decision", reasoning["enabled"])
	}
}

// On OpenRouter a request carrying parameters asks to be routed to a host that
// supports them.
//
// One model id is served by several hosts and they do not accept the same
// controls. Without this the parameter is accepted by whichever answers,
// dropped by the ones that do not understand it, and the call proceeds as
// though nothing had been asked.
func TestParametersAskForAHostThatSupportsThem(t *testing.T) {
	withParams := bodyOf(t,
		func(url string) *Client {
			return NewClientWithProvider(ProviderOpenRouter, url, "k", "kimi").
				Parameters(func(string) map[string]any { return map[string]any{"thinking_budget": 128} })
		},
		&ChatRequest{Messages: []Message{{Role: "user", Content: "go"}}},
	)
	provider, ok := withParams["provider"].(map[string]any)
	if !ok || provider["require_parameters"] != true {
		t.Errorf("provider = %v, want require_parameters true", withParams["provider"])
	}

	// Nothing to require, nothing required: a request with no parameters keeps
	// the widest choice of hosts. The routing block itself may still be there —
	// the shipped block list puts one in — so what is checked is the flag.
	none := bodyOf(t,
		func(url string) *Client { return NewClientWithProvider(ProviderOpenRouter, url, "k", "kimi") },
		&ChatRequest{Messages: []Message{{Role: "user", Content: "go"}}},
	)
	if p, ok := none["provider"].(map[string]any); ok && p["require_parameters"] == true {
		t.Errorf("hosts were narrowed to those supporting parameters, and there are none: %v", p)
	}

	// And only on OpenRouter — every other upstream would reject the field or
	// drop it.
	plain := bodyOf(t,
		func(url string) *Client {
			return NewClient(url, "k", "kimi").
				Parameters(func(string) map[string]any { return map[string]any{"thinking_budget": 128} })
		},
		&ChatRequest{Messages: []Message{{Role: "user", Content: "go"}}},
	)
	if _, present := plain["provider"]; present {
		t.Errorf("provider routing = %v sent to an upstream that is not OpenRouter", plain["provider"])
	}
}

// No lookup, no change: a request goes exactly as its caller wrote it.
func TestNoParameterLookupSendsNothingExtra(t *testing.T) {
	body := bodyOf(t,
		func(url string) *Client { return NewClient(url, "k", "kimi") },
		&ChatRequest{Messages: []Message{{Role: "user", Content: "go"}}},
	)
	for _, k := range []string{"thinking_budget", "enable_thinking", "provider"} {
		if _, present := body[k]; present {
			t.Errorf("%s was sent with no catalog behind it", k)
		}
	}
}

// A streamed call carries the same routing and the same parameters.
//
// routeProviders was applied only on the non-streamed door, so a blocked host
// was refused for the aggregator's call and allowed for the planner's — and
// with the planner, the chat lane and the compute lane all streaming now, that
// is most of a run.
func TestAStreamedCallCarriesTheSameRoutingAndParameters(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := NewClientWithProvider(ProviderOpenRouter, srv.URL, "k", "kimi").
		Parameters(func(string) map[string]any { return map[string]any{"thinking_budget": 6144} })
	if _, err := c.CompleteStreamResp(context.Background(),
		&ChatRequest{Messages: []Message{{Role: "user", Content: "go"}}}, nil); err != nil {
		t.Fatalf("streamed call failed: %v", err)
	}

	if got := body["thinking_budget"]; got != float64(6144) {
		t.Errorf("thinking_budget = %v on a streamed call, want 6144", got)
	}
	provider, ok := body["provider"].(map[string]any)
	if !ok {
		t.Fatalf("no provider routing on a streamed call: %v", body["provider"])
	}
	if provider["require_parameters"] != true {
		t.Errorf("require_parameters = %v on a streamed call", provider["require_parameters"])
	}
	if _, blocked := provider["ignore"]; !blocked {
		t.Error("the block list did not reach a streamed call")
	}
}
