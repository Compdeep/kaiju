package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

/*
 * What a provider served from its cache reaches Usage.
 *
 * Nothing recorded it, so whether a prompt was cached could not be told from
 * anything kaiju kept: OpenRouter, OpenAI and Anthropic all report it on every
 * reply, and the client dropped it on the way in.
 */

// serve answers every request with body.
func serve(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// OpenRouter and OpenAI put it in prompt_tokens_details, inside prompt_tokens.
func TestCachedTokensAreReadFromTheOpenAIShape(t *testing.T) {
	srv := serve(t, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":10339,"completion_tokens":60,"total_tokens":10399,
		"prompt_tokens_details":{"cached_tokens":10318,"cache_write_tokens":7}}}`)
	resp, err := NewClient(srv.URL, "k", "m").Complete(context.Background(),
		&ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Usage.CachedTokens(); got != 10318 {
		t.Errorf("cached = %d, want 10318", got)
	}
	if got := resp.Usage.CacheWriteTokens(); got != 7 {
		t.Errorf("cache writes = %d, want 7", got)
	}
	if resp.Usage.PromptTokens != 10339 {
		t.Errorf("prompt = %d, want 10339", resp.Usage.PromptTokens)
	}
}

// Anthropic counts cache reads and writes beside input_tokens, not inside it,
// so the prompt is the three together: what PromptTokens means everywhere else.
func TestCachedTokensAreReadFromAnthropic(t *testing.T) {
	srv := serve(t, `{"id":"msg_1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",
		"usage":{"input_tokens":21,"output_tokens":60,"cache_read_input_tokens":10000,"cache_creation_input_tokens":300}}`)
	resp, err := NewClientWithProvider(ProviderAnthropic, srv.URL, "k", "m").Complete(context.Background(),
		&ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Usage.CachedTokens(); got != 10000 {
		t.Errorf("cached = %d, want 10000", got)
	}
	if got := resp.Usage.CacheWriteTokens(); got != 300 {
		t.Errorf("cache writes = %d, want 300", got)
	}
	if resp.Usage.PromptTokens != 10321 || resp.Usage.TotalTokens != 10381 {
		t.Errorf("prompt = %d, total = %d, want 10321 and 10381", resp.Usage.PromptTokens, resp.Usage.TotalTokens)
	}
}

// A reply that says nothing about caching reads as none.
func TestNoCacheDetailReadsAsNone(t *testing.T) {
	srv := serve(t, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`)
	resp, err := NewClient(srv.URL, "k", "m").Complete(context.Background(),
		&ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.CachedTokens() != 0 || resp.Usage.CacheWriteTokens() != 0 {
		t.Errorf("cache detail invented: %+v", resp.Usage)
	}
}
