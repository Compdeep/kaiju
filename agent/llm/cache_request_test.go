package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

/*
 * A model that caches only when asked is asked, on every call.
 *
 * Anthropic's models cache a prompt only when the request carries
 * cache_control; without it every call is read in full. One field at the top of
 * the request asks for the whole prompt up to its last block, and the provider
 * moves that point forward as a run grows. OpenRouter passes it to Anthropic
 * and translates it for Bedrock.
 */

// bodyFor sends one request for model through provider, with a catalog saying
// whether the model caches on request, and returns what reached the wire.
func bodyFor(t *testing.T, provider string, onRequest bool) map[string]any {
	t.Helper()
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		if provider == ProviderAnthropic {
			_, _ = w.Write([]byte(`{"id":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	c := NewClientWithProvider(provider, srv.URL, "k", "claude").Catalog(func(string) (ModelFacts, bool) {
		return ModelFacts{CacheOnRequest: onRequest}, true
	})
	if _, err := c.Complete(context.Background(), &ChatRequest{Messages: []Message{
		{Role: "system", Content: "rules"}, {Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return sent
}

func TestAModelThatCachesOnRequestIsAsked(t *testing.T) {
	for _, p := range []string{ProviderOpenRouter, ProviderAnthropic} {
		cc, _ := bodyFor(t, p, true)["cache_control"].(map[string]any)
		if cc["type"] != "ephemeral" {
			t.Errorf("%s: no cache_control on a model that caches only when asked", p)
		}
	}
}

func TestAModelThatCachesByItselfIsNotAsked(t *testing.T) {
	for _, p := range []string{ProviderOpenRouter, ProviderAnthropic} {
		if _, there := bodyFor(t, p, false)["cache_control"]; there {
			t.Errorf("%s: cache_control sent to a model the catalog does not mark", p)
		}
	}
}
