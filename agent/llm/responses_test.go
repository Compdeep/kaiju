package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// roundTrip answers every request with one canned reply and keeps what was
// sent, so a client pointed at api.openai.com never leaves the test.
type roundTrip struct {
	path, body string
	reply      string
	stream     bool
}

func (rt *roundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	rt.path, rt.body = r.URL.Path, string(b)
	h := http.Header{"Content-Type": {"application/json"}}
	if rt.stream {
		h.Set("Content-Type", "text/event-stream")
	}
	return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(rt.reply)), Request: r}, nil
}

func openAIClient(rt http.RoundTripper, facts Catalog) *Client {
	return NewClientWithProvider(ProviderOpenAI, "https://api.openai.com/v1", "k", "gpt-x").Catalog(facts).Transport(rt)
}

// The planner's request as OpenAI's Responses endpoint takes it: the reply
// cap as max_output_tokens, a flat function tool sent non-strict, the forced
// tool by name, the effort beside the tools, the earlier tool call and its
// reply as items, and no temperature for a model that thinks.
func TestResponsesBody(t *testing.T) {
	thinks := func(string) (ModelFacts, bool) { return ModelFacts{Thinking: Thinking{Default: true}}, true }
	req := &ChatRequest{
		Model: "gpt-x", MaxTokens: 512, Temperature: 0.3, ReasoningEffort: "low", CacheKey: "run-1",
		Tools:      []ToolDef{{Type: "function", Function: FunctionDef{Name: "plan", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}}},
		ToolChoice: ForceToolChoice("plan"),
		Messages: []Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: FunctionCall{Name: "plan", Arguments: `{"a":1}`}}}},
			{Role: "tool", ToolCallID: "c1", Content: "done"},
		},
	}
	body, err := openAIClient(nil, thinks).responsesBody(req, false)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["max_output_tokens"] != float64(512) || got["max_tokens"] != nil {
		t.Errorf("cap: %s", body)
	}
	if _, has := got["temperature"]; has {
		t.Errorf("a thinking model was sent a temperature: %s", body)
	}
	if r, _ := got["reasoning"].(map[string]any); r["effort"] != "low" {
		t.Errorf("effort: %s", body)
	}
	tool := got["tools"].([]any)[0].(map[string]any)
	if tool["name"] != "plan" || tool["strict"] != false || tool["function"] != nil {
		t.Errorf("tool: %v", tool)
	}
	if tc, _ := got["tool_choice"].(map[string]any); tc["name"] != "plan" || tc["function"] != nil {
		t.Errorf("tool_choice: %v", got["tool_choice"])
	}
	if got["prompt_cache_key"] != "run-1" || got["store"] != false {
		t.Errorf("cache key or store: %s", body)
	}
	in := got["input"].([]any)
	types := []string{}
	for _, it := range in {
		types = append(types, it.(map[string]any)["type"].(string))
	}
	if strings.Join(types, ",") != "message,message,function_call,function_call_output" {
		t.Errorf("input items: %v", types)
	}
	if fc := in[2].(map[string]any); fc["call_id"] != "c1" || fc["arguments"] != `{"a":1}` {
		t.Errorf("function_call: %v", fc)
	}
	if req.MaxTokens != 512 || req.Temperature != 0.3 {
		t.Errorf("request changed")
	}

	plain := func(string) (ModelFacts, bool) { return ModelFacts{}, true }
	body, _ = openAIClient(nil, plain).responsesBody(req, false)
	got = nil
	_ = json.Unmarshal(body, &got)
	if got["temperature"] != 0.3 {
		t.Errorf("a model that does not think lost its temperature: %s", body)
	}
	body, _ = openAIClient(nil, nil).responsesBody(req, false)
	got = nil
	_ = json.Unmarshal(body, &got)
	if _, has := got["temperature"]; has {
		t.Errorf("a model the catalog does not know was sent a temperature: %s", body)
	}
}

// A schema request goes out as text.format.
func TestResponsesBodySchema(t *testing.T) {
	req := &ChatRequest{Model: "gpt-x", ResponseFormat: &ResponseFormat{Type: "json_schema",
		JSONSchema: &JSONSchema{Name: "plan", Strict: true, Schema: json.RawMessage(`{"type":"object"}`)}}}
	body, _ := openAIClient(nil, nil).responsesBody(req, false)
	var got struct {
		Text struct {
			Format map[string]any `json:"format"`
		} `json:"text"`
	}
	_ = json.Unmarshal(body, &got)
	if got.Text.Format["type"] != "json_schema" || got.Text.Format["name"] != "plan" || got.Text.Format["strict"] != true {
		t.Fatalf("format: %s", body)
	}
}

// A reply with thinking, a tool call and usage comes back as the same
// ChatResponse chat completions gives, from /v1/responses.
func TestCompleteOnOpenAIUsesResponses(t *testing.T) {
	rt := &roundTrip{reply: `{"id":"resp_1","status":"completed","output":[
		{"type":"reasoning","summary":[{"type":"summary_text","text":"thought"}]},
		{"type":"function_call","call_id":"call_9","name":"plan","arguments":"{\"steps\":[]}"}],
		"usage":{"input_tokens":100,"output_tokens":40,"total_tokens":140,
		"input_tokens_details":{"cached_tokens":64},"output_tokens_details":{"reasoning_tokens":30}}}`}
	resp, err := openAIClient(rt, nil).completeOpenAI(context.Background(), &ChatRequest{Model: "gpt-x",
		Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if rt.path != "/v1/responses" {
		t.Errorf("sent to %s", rt.path)
	}
	c := resp.Choices[0]
	if c.FinishReason != "tool_calls" || len(c.Message.ToolCalls) != 1 || c.Message.ToolCalls[0].ID != "call_9" ||
		c.Message.ToolCalls[0].Function.Arguments != `{"steps":[]}` || c.Message.Reasoning != "thought" {
		t.Errorf("choice: %+v", c)
	}
	u := resp.Usage
	if u.PromptTokens != 100 || u.CompletionTokens != 40 || u.CachedTokens() != 64 || u.ReasoningTokens() != 30 {
		t.Errorf("usage: %+v", u)
	}
}

// A reply cut at the cap is "length", and a 200 carrying an error is an error.
func TestResponsesEndings(t *testing.T) {
	rt := &roundTrip{reply: `{"id":"r","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},
		"output":[{"type":"message","content":[{"type":"output_text","text":"par"}]}],"usage":{}}`}
	resp, err := openAIClient(rt, nil).completeOpenAI(context.Background(), &ChatRequest{Model: "gpt-x"})
	if err != nil || resp.Choices[0].FinishReason != "length" || resp.Choices[0].Message.Content != "par" {
		t.Errorf("incomplete: %v %+v", err, resp)
	}
	rt.reply = `{"id":"r","status":"failed","error":{"message":"boom","type":"server_error"},"output":[]}`
	if _, err := openAIClient(rt, nil).completeOpenAI(context.Background(), &ChatRequest{Model: "gpt-x"}); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Errorf("failed reply: %v", err)
	}
}

// A streamed reply assembles text, thinking and a tool call from its events,
// and takes usage from the completed event.
func TestStreamOnOpenAIUsesResponses(t *testing.T) {
	ev := func(s string) string { return "event: x\ndata: " + s + "\n\n" }
	rt := &roundTrip{stream: true, reply: ev(`{"type":"response.reasoning_summary_text.delta","delta":"hm"}`) +
		ev(`{"type":"response.output_text.delta","delta":"Hel"}`) +
		ev(`{"type":"response.output_text.delta","delta":"lo"}`) +
		ev(`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","call_id":"c1","name":"plan","arguments":""}}`) +
		ev(`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"a\""}`) +
		ev(`{"type":"response.function_call_arguments.delta","output_index":2,"delta":":1}"}`) +
		ev(`{"type":"response.completed","response":{"id":"resp_2","status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`)}
	var shown strings.Builder
	resp, err := openAIClient(rt, nil).completeStreamResp(context.Background(), &ChatRequest{Model: "gpt-x"},
		func(c, kind string) {
			if kind == "content" {
				shown.WriteString(c)
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if rt.path != "/v1/responses" || !strings.Contains(rt.body, `"stream":true`) {
		t.Errorf("sent to %s: %s", rt.path, rt.body)
	}
	m := resp.Choices[0].Message
	if m.Content != "Hello" || shown.String() != "Hello" || m.Reasoning != "hm" || len(m.ToolCalls) != 1 ||
		m.ToolCalls[0].Function.Arguments != `{"a":1}` || m.ToolCalls[0].ID != "c1" {
		t.Errorf("message: %+v", m)
	}
	if resp.ID != "resp_2" || resp.Usage.TotalTokens != 15 || resp.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("reply: %+v", resp)
	}
}

// Another OpenAI-compatible endpoint stays on chat completions.
func TestSelfHostedStaysOnChatCompletions(t *testing.T) {
	rt := &roundTrip{reply: `{"id":"x","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`}
	c := NewClientWithProvider(ProviderOpenAI, "http://localhost:8000/v1", "k", "m").Transport(rt)
	if _, err := c.completeOpenAI(context.Background(), &ChatRequest{Model: "m", MaxTokens: 9}); err != nil {
		t.Fatal(err)
	}
	if rt.path != "/v1/chat/completions" || !strings.Contains(rt.body, `"max_tokens":9`) {
		t.Errorf("sent to %s: %s", rt.path, rt.body)
	}
}
