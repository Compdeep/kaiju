package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/*
 * One run's calls carry one key, so a gateway keeps them on one provider.
 *
 * A provider's cache is its own. OpenRouter keeps a conversation on one
 * provider only while it can tell the requests belong together, and with no
 * key it guesses from the opening messages, which differ from stage to stage:
 * consecutive calls of one investigation changed provider 136 times in 264.
 * OpenRouter and OpenAI both read prompt_cache_key.
 */

// sentBody answers like a provider and returns what reached it.
func sentBody(t *testing.T, provider string, req *ChatRequest) string {
	t.Helper()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		if provider == ProviderAnthropic {
			_, _ = w.Write([]byte(`{"id":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	c := NewClientWithProvider(provider, srv.URL, "k", "m")
	if provider == "openai-api" {
		// OpenAI's own API is told apart from other OpenAI-compatible
		// endpoints by its address, which a test server cannot have.
		c = NewClientWithProvider(ProviderOpenAI, srv.URL, "k", "m")
		c.endpoint = srv.URL + "/api.openai.com"
	}
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestTheCacheKeyReachesOpenRouterAndOpenAI(t *testing.T) {
	for _, p := range []string{ProviderOpenRouter, "openai-api"} {
		body := sentBody(t, p, &ChatRequest{CacheKey: "run-7", Messages: []Message{{Role: "user", Content: "hi"}}})
		var sent map[string]any
		if err := json.Unmarshal([]byte(body), &sent); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if sent["prompt_cache_key"] != "run-7" {
			t.Errorf("%s was not given the run's key: %s", p, body)
		}
	}
}

// A self-hosted OpenAI-compatible endpoint, or Anthropic, is not sent a field
// it does not read.
func TestTheCacheKeyGoesNowhereElse(t *testing.T) {
	for _, p := range []string{"", ProviderOpenAI, ProviderAnthropic} {
		if body := sentBody(t, p, &ChatRequest{CacheKey: "run-7", Messages: []Message{{Role: "user", Content: "hi"}}}); strings.Contains(body, "run-7") {
			t.Errorf("provider %q was sent the key: %s", p, body)
		}
	}
}
