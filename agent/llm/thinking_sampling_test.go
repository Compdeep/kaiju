package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A thinking call is sampled the way the model's maker says.
//
// qwen/qwen3.6-35b-a3b planning at temperature 0.3 and nothing else wrote one
// tool again and again with a counter in the tag until the cap, on 44 of 140
// live plans across four providers; at its published 0.6, top_p 0.95, top_k 20,
// on 6 of 138 (5 November 2026). The settings are the catalog's, so a model
// without them is sent what its caller wrote.
func TestAThinkingCallIsSampledAsItsMakerSays(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = nil
		_ = json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	published := Sampling{Temperature: 0.6, TopP: 0.95, TopK: 20}
	catalog := func(model string) (ModelFacts, bool) {
		f := ModelFacts{Thinking: Thinking{Default: true, Optional: true}}
		if model == "with-sampling" {
			f.Thinking.Sampling = published
		}
		return f, true
	}
	send := func(model string, want Want) map[string]any {
		c := NewClient(srv.URL, "k", model).Catalog(catalog)
		_, err := c.Complete(context.Background(), &ChatRequest{
			Messages: []Message{{Role: "user", Content: "plan"}}, Temperature: 0.3, MaxTokens: 100,
			Think: &Reasoning{Want: want},
		})
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		return got
	}

	on := send("with-sampling", WantOn)
	if on["temperature"] != 0.6 || on["top_p"] != 0.95 || on["top_k"] != float64(20) {
		t.Errorf("a thinking call sent temperature %v, top_p %v, top_k %v; want the published 0.6, 0.95, 20",
			on["temperature"], on["top_p"], on["top_k"])
	}
	off := send("with-sampling", WantOff)
	if off["temperature"] != 0.3 || off["top_p"] != nil || off["top_k"] != nil {
		t.Errorf("a call with thinking off sent temperature %v, top_p %v, top_k %v; want the caller's 0.3 and nothing else",
			off["temperature"], off["top_p"], off["top_k"])
	}
	none := send("without-sampling", WantOn)
	if none["temperature"] != 0.3 || none["top_p"] != nil || none["top_k"] != nil {
		t.Errorf("a model with no published sampling sent temperature %v, top_p %v, top_k %v; want the caller's 0.3 and nothing else",
			none["temperature"], none["top_p"], none["top_k"])
	}
}
