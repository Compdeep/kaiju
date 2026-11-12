package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const llama = "us.meta.llama3-3-70b-instruct-v1:0"

func llamaFacts(string) (ModelFacts, bool) { return ModelFacts{Converse: true}, true }

func novaFacts(string) (ModelFacts, bool) {
	return ModelFacts{Converse: true, Thinking: Thinking{Optional: true, Efforts: []Effort{EffortLow, EffortMedium, EffortHigh}}}, true
}

// converseServer answers every call with reply, keeping the path and body.
func converseServer(t *testing.T, reply string, path *string, body *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(body); err != nil {
			t.Errorf("request body: %v", err)
		}
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(reply))
	}))
}

func converseClient(srv *httptest.Server, model string, facts Catalog) *Client {
	return NewClientWithProvider(ProviderBedrock, srv.URL+"/v1", "key", model).Catalog(facts)
}

const plainReply = `{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":3,"outputTokens":1}}`

// The request in Converse's shape: system apart, tool calls and results as
// blocks, one message per turn of the conversation, the forced tool named.
func TestConverseRequest(t *testing.T) {
	var path string
	var body map[string]any
	srv := converseServer(t, plainReply, &path, &body)
	defer srv.Close()

	req := &ChatRequest{
		Messages: []Message{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "look"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "t1", Function: FunctionCall{Name: "ls", Arguments: `{"dir":"/"}`}}}},
			{Role: "tool", ToolCallID: "t1", Content: "etc"},
			{Role: "user", Parts: []ContentPart{{Type: "text", Text: "and this"}, {Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,AAAA"}}}},
		},
		Tools:       []ToolDef{{Type: "function", Function: FunctionDef{Name: "ls", Description: "list", Parameters: json.RawMessage(`{"type":"object"}`)}}},
		ToolChoice:  ForceToolChoice("ls"),
		MaxTokens:   500,
		Temperature: 0.2,
	}
	if _, err := converseClient(srv, llama, llamaFacts).Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if path != "/model/"+llama+"/converse" {
		t.Errorf("path = %s", path)
	}
	got, _ := json.Marshal(body) // re-encoded with sorted keys, so parts compare as text

	for _, part := range []string{
		`"system":[{"text":"be brief"}]`,
		`"inferenceConfig":{"maxTokens":500,"temperature":0.2}`,
		`"toolChoice":{"tool":{"name":"ls"}}`,
		`"toolSpec":{"description":"list","inputSchema":{"json":{"type":"object"}},"name":"ls"}`,
		`{"content":[{"text":"look"}],"role":"user"}`,
		`{"content":[{"toolUse":{"input":{"dir":"/"},"name":"ls","toolUseId":"t1"}}],"role":"assistant"}`,
		`{"content":[{"toolResult":{"content":[{"text":"etc"}],"toolUseId":"t1"}},{"text":"and this"},{"image":{"format":"png","source":{"bytes":"AAAA"}}}],"role":"user"}`,
	} {
		if !strings.Contains(string(got), part) {
			t.Errorf("body lacks %s\nbody: %s", part, got)
		}
	}
	if _, ok := body["additionalModelRequestFields"]; ok {
		t.Error("thinking sent to a model with none")
	}
}

// A stage forcing a form keeps it as a forced tool on Converse, which has no
// strict schema for these models. Without it the request carries the earlier
// steps' tool calls and no toolConfig, and Bedrock refuses it.
func TestConverseKeepsTheForcedForm(t *testing.T) {
	var path string
	var body map[string]any
	srv := converseServer(t, plainReply, &path, &body)
	defer srv.Close()
	req := &ChatRequest{
		Messages: []Message{
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "s1", Function: FunctionCall{Name: "compute", Arguments: `{}`}}}},
			{Role: "tool", ToolCallID: "s1", Content: `{"error":"blocked"}`},
			{Role: "user", Content: "decide"},
		},
		Tools: []ToolDef{{Type: "function", Function: FunctionDef{Name: "reflect", Parameters: json.RawMessage(
			`{"type":"object","properties":{"decision":{"type":"string"}},"required":["decision"]}`)}}},
		ToolChoice: "required",
	}
	if _, err := converseClient(srv, "us.amazon.nova-2-lite-v1:0", novaFacts).Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(body["toolConfig"])
	if !strings.Contains(string(got), `"name":"reflect"`) || !strings.Contains(string(got), `"toolChoice":{"any":{}}`) {
		t.Errorf("toolConfig = %s", got)
	}
}

// Converse refuses a conversation that starts with the assistant, which is how
// a stage's request starts when earlier steps come before the objective.
func TestConverseStartsWithTheUser(t *testing.T) {
	req := &ChatRequest{Messages: []Message{
		{Role: "system", Content: "decide"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "s1", Function: FunctionCall{Name: "compute", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "s1", Content: "blocked"},
		{Role: "user", Content: "what next"},
	}}
	out := buildConverseRequest(req)
	if len(out.Messages) != 3 || out.Messages[0].Role != "user" || out.Messages[0].Content[0].Text != converseOpening ||
		out.Messages[1].Role != "assistant" {
		t.Fatalf("messages = %+v", out.Messages)
	}

	// A conversation that already starts with the user is left as it is.
	plain := buildConverseRequest(&ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if len(plain.Messages) != 1 || plain.Messages[0].Content[0].Text != "hi" {
		t.Errorf("messages = %+v", plain.Messages)
	}
}

// Thinking goes to Nova 2 as its reasoning config.
func TestConverseThinking(t *testing.T) {
	var path string
	var body map[string]any
	srv := converseServer(t, plainReply, &path, &body)
	defer srv.Close()
	_, err := converseClient(srv, "us.amazon.nova-2-lite-v1:0+medium", novaFacts).Complete(context.Background(),
		&ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(body["additionalModelRequestFields"])
	if string(got) != `{"reasoningConfig":{"maxReasoningEffort":"medium","type":"enabled"}}` {
		t.Errorf("additionalModelRequestFields = %s", got)
	}
}

// The reply in kaiju's shape.
func TestConverseReply(t *testing.T) {
	var path string
	var body map[string]any
	srv := converseServer(t, `{"output":{"message":{"role":"assistant","content":[
		{"reasoningContent":{"reasoningText":{"text":"hmm"}}},{"text":"listing"},
		{"toolUse":{"toolUseId":"t2","name":"ls","input":{"dir":"/"}}}]}},
		"stopReason":"tool_use","usage":{"inputTokens":10,"outputTokens":5,"cacheReadInputTokens":4}}`, &path, &body)
	defer srv.Close()
	resp, err := converseClient(srv, llama, llamaFacts).Complete(context.Background(),
		&ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	c := resp.Choices[0]
	if c.Message.Content != "listing" || c.Message.Reasoning != "hmm" || c.FinishReason != "tool_calls" {
		t.Errorf("choice = %+v", c)
	}
	if len(c.Message.ToolCalls) != 1 || c.Message.ToolCalls[0].ID != "t2" || c.Message.ToolCalls[0].Function.Arguments != `{"dir":"/"}` {
		t.Errorf("tool calls = %+v", c.Message.ToolCalls)
	}
	if resp.Usage.PromptTokens != 14 || resp.Usage.CompletionTokens != 5 || resp.Usage.CachedTokens() != 4 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

// A Bedrock model the catalog does not mark stays on Chat Completions; a
// mantle endpoint's Converse calls go to bedrock-runtime in the same region.
func TestConverseRouting(t *testing.T) {
	var path string
	var body map[string]any
	srv := converseServer(t, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, &path, &body)
	defer srv.Close()
	if _, err := converseClient(srv, "qwen.qwen3-32b", qwenOnBedrock).Complete(context.Background(),
		&ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if path != "/v1/chat/completions" {
		t.Errorf("path = %s", path)
	}
	c := NewClientWithProvider(ProviderBedrock, BedrockEndpoint("eu-west-1"), "k", llama)
	if got, want := c.converseURL(llama, true), "https://bedrock-runtime.eu-west-1.amazonaws.com/model/"+llama+"/converse-stream"; got != want {
		t.Errorf("converseURL = %s, want %s", got, want)
	}
}

// Bedrock's refusals are kaiju's error kinds.
func TestConverseErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"Too many requests"}`))
	}))
	defer srv.Close()
	_, err := converseClient(srv, llama, llamaFacts).completeConverse(context.Background(),
		&ChatRequest{Model: llama, Messages: []Message{{Role: "user", Content: "hi"}}})
	var ce *CallError
	if !errors.As(err, &ce) || ce.Kind != KindRateLimited {
		t.Errorf("err = %v, want rate limited", err)
	}
}

// A streamed call shows text and thinking as they arrive and ends with the
// same reply a plain call gives.
func TestConverseStream(t *testing.T) {
	ev := func(kind, payload string) []byte {
		return encodeEvent(map[string]string{":event-type": kind, ":message-type": "event", ":content-type": "application/json"}, []byte(payload))
	}
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		for _, b := range [][]byte{
			ev("messageStart", `{"role":"assistant"}`),
			ev("contentBlockDelta", `{"contentBlockIndex":0,"delta":{"reasoningContent":{"text":"hm"}}}`),
			ev("contentBlockDelta", `{"contentBlockIndex":1,"delta":{"text":"Hel"}}`),
			ev("contentBlockDelta", `{"contentBlockIndex":1,"delta":{"text":"lo"}}`),
			ev("contentBlockStart", `{"contentBlockIndex":2,"start":{"toolUse":{"toolUseId":"t3","name":"ls"}}}`),
			ev("contentBlockDelta", `{"contentBlockIndex":2,"delta":{"toolUse":{"input":"{\"dir\":"}}}`),
			ev("contentBlockDelta", `{"contentBlockIndex":2,"delta":{"toolUse":{"input":"\"/\"}"}}}`),
			ev("messageStop", `{"stopReason":"tool_use"}`),
			ev("metadata", `{"usage":{"inputTokens":7,"outputTokens":3}}`),
		} {
			_, _ = w.Write(b)
		}
	}))
	defer srv.Close()

	var shown []string
	resp, err := converseClient(srv, llama, llamaFacts).CompleteStreamResp(context.Background(),
		&ChatRequest{Messages: []Message{{Role: "user", Content: "hi"}}},
		func(chunk, kind string) { shown = append(shown, kind+":"+chunk) })
	if err != nil {
		t.Fatal(err)
	}
	if path != "/model/"+llama+"/converse-stream" {
		t.Errorf("path = %s", path)
	}
	if strings.Join(shown, "|") != "reasoning:hm|content:Hel|content:lo" {
		t.Errorf("shown = %v", shown)
	}
	c := resp.Choices[0]
	if c.Message.Content != "Hello" || c.Message.Reasoning != "hm" || c.FinishReason != "tool_calls" {
		t.Errorf("choice = %+v", c)
	}
	if len(c.Message.ToolCalls) != 1 || c.Message.ToolCalls[0].ID != "t3" || c.Message.ToolCalls[0].Function.Arguments != `{"dir":"/"}` {
		t.Errorf("tool calls = %+v", c.Message.ToolCalls)
	}
	if resp.Usage.PromptTokens != 7 || resp.Usage.CompletionTokens != 3 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

// An error mid-stream ends the call as the error it is, keeping what arrived.
func TestConverseStreamException(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(encodeEvent(map[string]string{":event-type": "contentBlockDelta", ":message-type": "event"},
			[]byte(`{"contentBlockIndex":0,"delta":{"text":"part"}}`)))
		_, _ = w.Write(encodeEvent(map[string]string{":message-type": "exception", ":exception-type": "throttlingException"},
			[]byte(`{"message":"slow down"}`)))
	}))
	defer srv.Close()
	resp, err := converseClient(srv, llama, llamaFacts).streamConverse(context.Background(),
		&ChatRequest{Model: llama, Messages: []Message{{Role: "user", Content: "hi"}}}, nil)
	var ce *CallError
	if !errors.As(err, &ce) || ce.Kind != KindRateLimited {
		t.Errorf("err = %v, want rate limited", err)
	}
	if resp == nil || resp.Choices[0].Message.Content != "part" {
		t.Errorf("what arrived was lost: %+v", resp)
	}
}
