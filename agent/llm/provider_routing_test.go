package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// The field is absent from the wire unless something is in it.
//
// This is the whole risk in the feature. OpenRouter reads an empty `only` as
// "no provider is allowed" and answers 404 with "All providers have been
// ignored" — so a list that failed to parse, or shipped empty, must reach the
// wire as nothing at all rather than as `[]`. Marshalling is checked rather than
// the struct, because `[]` and absent are the same value in Go and different
// bytes on the wire.
func TestAnEmptyListIsNotSent(t *testing.T) {
	for _, c := range []struct {
		name string
		req  ChatRequest
	}{
		{"no routing at all", ChatRequest{Model: "m"}},
		{"an empty block", ChatRequest{Model: "m", Provider: &ProviderRouting{}}},
		{"empty slices", ChatRequest{Model: "m", Provider: &ProviderRouting{Only: []string{}, Ignore: []string{}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			body, err := json.Marshal(c.req)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			for _, forbidden := range []string{`"only"`, `"ignore"`} {
				if strings.Contains(string(body), forbidden) {
					t.Errorf("%s reached the wire: %s", forbidden, body)
				}
			}
		})
	}
}

func TestTheListsReachTheWireWhenSet(t *testing.T) {
	body, err := json.Marshal(ChatRequest{
		Model:    "m",
		Provider: &ProviderRouting{Ignore: []string{"alibaba", "siliconflow/fp8"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"provider":{"ignore":["alibaba","siliconflow/fp8"]}`) {
		t.Errorf("wrong shape for OpenRouter: %s", body)
	}
}

// Only OpenRouter is told about providers. Sending the field to OpenAI or a
// self-hosted endpoint is at best ignored and at worst a rejected request.
func TestOnlyOpenRouterIsRouted(t *testing.T) {
	for _, provider := range []string{ProviderOpenAI, ProviderAnthropic, ""} {
		c := &Client{provider: provider}
		req := &ChatRequest{Model: "m"}
		c.routeProviders(req)
		if req.Provider != nil {
			t.Errorf("provider %q was given routing it did not ask for", provider)
		}
	}
}

// A caller that made its own routing decision keeps it. The lists are the
// default for calls that said nothing, not an override.
func TestACallersOwnRoutingIsLeftAlone(t *testing.T) {
	c := &Client{provider: ProviderOpenRouter}
	mine := &ProviderRouting{Only: []string{"nebius/fp8"}}
	req := &ChatRequest{Model: "m", Provider: mine}
	c.routeProviders(req)
	if req.Provider != mine {
		t.Errorf("the caller's routing was replaced: %+v", req.Provider)
	}
}

// A stock build sends the shipped blacklist and no whitelist. What is on that
// list, and why, is models/openrouter/routing_test.go; what matters here is the
// shape that reaches the wire — ignore populated, only omitted, because an
// empty only is read by OpenRouter as "no provider is allowed" and answers 404.
func TestTheStockBuildSendsTheShippedBlacklist(t *testing.T) {
	c := &Client{provider: ProviderOpenRouter}
	req := &ChatRequest{Model: "m"}
	c.routeProviders(req)
	if req.Provider == nil {
		t.Fatal("a stock build sent no routing; the shipped blacklist is not reaching the wire")
	}
	if len(req.Provider.Ignore) == 0 {
		t.Errorf("ignore is empty: %+v", req.Provider)
	}
	if req.Provider.Only != nil {
		t.Errorf("only must stay absent, or OpenRouter refuses every provider: %+v", req.Provider)
	}
}
