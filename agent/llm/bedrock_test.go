package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The Qwen3 32B facts as measured on Bedrock: it does not think unless asked,
// and only "high" makes it.
func qwenOnBedrock(string) (ModelFacts, bool) {
	return ModelFacts{Thinking: Thinking{Optional: true, Efforts: []Effort{EffortHigh}}}, true
}

// gpt-oss on Bedrock: thinks always, at the effort it is given.
func gptOSSOnBedrock(string) (ModelFacts, bool) {
	return ModelFacts{Thinking: Thinking{Default: true, Efforts: []Effort{EffortLow, EffortMedium, EffortHigh}}}, true
}

// sendBedrock makes one call to a capturing server as a Bedrock client and
// returns the body the server received.
func sendBedrock(t *testing.T, model string, facts Catalog, think *Reasoning) map[string]any {
	t.Helper()
	var body map[string]any
	srv := bodyCapture(t, &body)
	defer srv.Close()
	c := NewClientWithProvider(ProviderBedrock, srv.URL+"/v1", "key", model).Catalog(facts)
	if _, err := c.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
		Think:    think,
		CacheKey: "run-1",
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return body
}

// Bedrock takes OpenAI's reasoning_effort, never OpenRouter's reasoning
// object, and is sent no cache key.
func TestBedrockSendsReasoningEffort(t *testing.T) {
	cases := []struct {
		name  string
		model string
		facts Catalog
		think *Reasoning
		want  any // nil: no reasoning_effort at all
	}{
		{"on, model off by default: its measured effort", "qwen.qwen3-32b", qwenOnBedrock, &Reasoning{Want: WantOn}, "high"},
		{"off, model off by default: nothing", "qwen.qwen3-32b", qwenOnBedrock, &Reasoning{Want: WantOff}, nil},
		{"nothing asked: nothing", "qwen.qwen3-32b", qwenOnBedrock, nil, nil},
		{"effort the model acts on", "openai.gpt-oss-120b", gptOSSOnBedrock, &Reasoning{Effort: EffortLow}, "low"},
		{"effort the model does not act on", "openai.gpt-oss-120b", gptOSSOnBedrock, &Reasoning{Effort: EffortMax}, nil},
		{"on, model on by default: nothing to switch", "openai.gpt-oss-120b", gptOSSOnBedrock, &Reasoning{Want: WantOn}, nil},
		{"off, model that thinks and can stop: none", "m", func(string) (ModelFacts, bool) {
			return ModelFacts{Thinking: Thinking{Default: true, Optional: true}}, true
		}, &Reasoning{Want: WantOff}, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := sendBedrock(t, tc.model, tc.facts, tc.think)
			if _, ok := body["reasoning"]; ok {
				t.Errorf("reasoning object sent to Bedrock: %v", body["reasoning"])
			}
			if got := body["reasoning_effort"]; got != tc.want {
				t.Errorf("reasoning_effort = %v, want %v", got, tc.want)
			}
			if _, ok := body["prompt_cache_key"]; ok {
				t.Error("prompt_cache_key sent to Bedrock")
			}
		})
	}
}

// Every other provider is unchanged: the reasoning object, no reasoning_effort.
func TestOtherProvidersKeepTheReasoningObject(t *testing.T) {
	var body map[string]any
	srv := bodyCapture(t, &body)
	defer srv.Close()
	c := NewClientWithProvider(ProviderOpenRouter, srv.URL, "k", "qwen/qwen3-32b").Catalog(qwenOnBedrock)
	if _, err := c.Complete(context.Background(), &ChatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}}, Think: &Reasoning{Want: WantOn},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Error("reasoning_effort sent to OpenRouter")
	}
	if _, ok := reasoningOnTheWire(t, body); !ok {
		t.Error("no reasoning object sent to OpenRouter")
	}
}

// A Bedrock client with no endpoint goes to us-east-1 with the key as a
// bearer token.
func TestBedrockDefaultEndpoint(t *testing.T) {
	c := NewClientWithProvider(ProviderBedrock, "", "key", "qwen.qwen3-32b")
	if got, want := c.chatURL(), "https://bedrock-mantle.us-east-1.api.aws/v1/chat/completions"; got != want {
		t.Errorf("chatURL = %s, want %s", got, want)
	}
	if got, want := BedrockEndpoint("eu-west-1"), "https://bedrock-mantle.eu-west-1.api.aws/v1"; got != want {
		t.Errorf("BedrockEndpoint = %s, want %s", got, want)
	}
	req, _ := http.NewRequest(http.MethodPost, c.chatURL(), nil)
	c.setAuthHeaders(req)
	if got := req.Header.Get("Authorization"); got != "Bearer key" {
		t.Errorf("Authorization = %q", got)
	}
}

// A forced single tool goes to Bedrock as a strict schema, as everywhere else.
// Sent as a plain tool call it is not enforced there, and a planner shown its
// earlier steps as calls writes the next step as a call instead of a plan.
func TestBedrockGetsTheStrictSchema(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"{\"a\":\"x\"}"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	c := NewClientWithProvider(ProviderBedrock, srv.URL+"/v1", "k", "qwen.qwen3-32b")
	req := oneTool(`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`)
	resp, err := c.Complete(context.Background(), req)
	if err != nil || len(resp.Choices) == 0 || len(resp.Choices[0].Message.ToolCalls) == 0 {
		t.Fatalf("the reply did not come back as the tool call: %v %+v", err, resp)
	}
	rf, ok := body["response_format"].(map[string]any)
	if !ok || rf["type"] != "json_schema" {
		t.Fatalf("response_format = %v, want a json_schema", body["response_format"])
	}
	if js, _ := rf["json_schema"].(map[string]any); js["strict"] != true {
		t.Errorf("schema not strict: %v", rf["json_schema"])
	}
}

// Thinking text comes back in reasoning_content as well as reasoning, streamed
// or not.
func TestReasoningContentIsRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok","reasoning_content":"thought"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	resp, err := NewClientWithProvider(ProviderBedrock, srv.URL+"/v1", "k", "m").Complete(context.Background(),
		&ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Choices[0].Message.Reasoning; got != "thought" {
		t.Errorf("Reasoning = %q, want thought", got)
	}

	stream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thought\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer stream.Close()
	sresp, err := NewClientWithProvider(ProviderBedrock, stream.URL+"/v1", "k", "m").CompleteStreamResp(context.Background(),
		&ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := sresp.Choices[0].Message.Reasoning; got != "thought" {
		t.Errorf("streamed Reasoning = %q, want thought", got)
	}
}
