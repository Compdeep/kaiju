package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A call that does not think is sampled the way the model's maker says too.
//
// Run 77f395aa (2026-11-21): the reflector on gpt-6-luna, thinking "none" at
// the caller's 0.3, wrote one complete decision and then the same decision 28
// more times, to its 8,192-token cap. Thinking.Sampling covers only the calls
// that think; Sampling is the other half, and a model with neither is sent
// what its caller wrote.
func TestACallThatDoesNotThinkIsSampledAsItsMakerSays(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = nil
		_ = json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	thinkingSampling := Sampling{Temperature: 0.6, TopP: 0.95, TopK: 20, PresencePenalty: 1.5}
	plain := Sampling{Temperature: 0.7, TopP: 0.8, TopK: 20}
	catalog := func(model string) (ModelFacts, bool) {
		switch model {
		case "hybrid": // thinks by default, can stop
			return ModelFacts{Thinking: Thinking{Default: true, Optional: true, Sampling: thinkingSampling}, Sampling: plain}, true
		case "instruct": // cannot think
			return ModelFacts{Sampling: plain}, true
		}
		return ModelFacts{Thinking: Thinking{Default: true, Optional: true}}, true
	}
	send := func(model string, want Want) map[string]any {
		c := NewClient(srv.URL, "k", model).Catalog(catalog)
		_, err := c.Complete(context.Background(), &ChatRequest{
			Messages: []Message{{Role: "user", Content: "decide"}}, Temperature: 0.3, MaxTokens: 100,
			Think: &Reasoning{Want: want},
		})
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		return got
	}

	off := send("hybrid", WantOff)
	if off["temperature"] != 0.7 || off["top_p"] != 0.8 || off["top_k"] != float64(20) || off["presence_penalty"] != nil {
		t.Errorf("thinking off sent temperature %v, top_p %v, top_k %v, presence_penalty %v; want the published 0.7, 0.8, 20 and none",
			off["temperature"], off["top_p"], off["top_k"], off["presence_penalty"])
	}
	on := send("hybrid", WantOn)
	if on["temperature"] != 0.6 || on["presence_penalty"] != 1.5 {
		t.Errorf("thinking on sent temperature %v, presence_penalty %v; want the thinking 0.6 and 1.5", on["temperature"], on["presence_penalty"])
	}
	instruct := send("instruct", WantAuto)
	if instruct["temperature"] != 0.7 || instruct["top_p"] != 0.8 {
		t.Errorf("a model that cannot think sent temperature %v, top_p %v; want the published 0.7, 0.8", instruct["temperature"], instruct["top_p"])
	}
	none := send("unpublished", WantOff)
	if none["temperature"] != 0.3 || none["top_p"] != nil {
		t.Errorf("a model with nothing published sent temperature %v, top_p %v; want the caller's 0.3 and nothing else", none["temperature"], none["top_p"])
	}
}

// On OpenAI's own API the reflector's call goes out with effort "none" and the
// published temperature, not the caller's 0.3.
func TestResponsesThinkingOffSendsThePublishedTemperature(t *testing.T) {
	facts := func(string) (ModelFacts, bool) {
		return ModelFacts{Thinking: Thinking{Default: true, Optional: true}, Sampling: Sampling{Temperature: 1}}, true
	}
	rt := &roundTrip{reply: `{"output":[{"type":"message","content":[{"type":"output_text","text":"{}"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`}
	_, err := openAIClient(rt, facts).Complete(context.Background(), WithoutReasoning(&ChatRequest{
		Messages: []Message{{Role: "user", Content: "decide"}}, Temperature: 0.3, MaxTokens: 300,
	}))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(rt.body), &got)
	if got["temperature"] != 1.0 {
		t.Errorf("temperature %v, want the published 1: %s", got["temperature"], rt.body)
	}
	if r, _ := got["reasoning"].(map[string]any); r["effort"] != "none" {
		t.Errorf("reasoning %v, want effort none", got["reasoning"])
	}
}

// A roleplay tune's min_p reaches the request, with its published temperature.
func TestMinPIsSentFromTheCatalog(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	catalog := func(string) (ModelFacts, bool) {
		return ModelFacts{Sampling: Sampling{Temperature: 1, MinP: 0.05}}, true
	}
	c := NewClient(srv.URL, "k", "thedrummer/cydonia-24b-v4.1").Catalog(catalog)
	if _, err := c.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}}, Temperature: 0.3, MaxTokens: 100,
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got["temperature"] != 1.0 || got["min_p"] != 0.05 || got["top_p"] != nil {
		t.Errorf("sent temperature %v, min_p %v, top_p %v; want 1, 0.05 and none", got["temperature"], got["min_p"], got["top_p"])
	}
}
