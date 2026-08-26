package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func oneTool(params string) *ChatRequest {
	return &ChatRequest{
		Tools: []ToolDef{{Type: "function", Function: FunctionDef{
			Name: "submit_preflight", Parameters: json.RawMessage(params)}}},
		ToolChoice: "required",
	}
}

// A stage asking for one shape is asking for a schema, however it is dressed.
// Every reasoning stage in this engine declares a single tool and forces the
// call; that is the request that gets the wire which actually enforces it.
func TestAsSchemaRequest_ConvertsAForcedSingleTool(t *testing.T) {
	req := oneTool(`{"type":"object","properties":{"mode":{"type":"string"},"intent":{"type":"string"}}}`)
	replaced := asSchemaRequest(req, "openai")

	if replaced == nil {
		t.Fatal("a forced single-tool call was left on the advisory wire")
	}
	if req.ResponseFormat == nil || req.ResponseFormat.Type != "json_schema" {
		t.Fatalf("no schema request was built: %+v", req.ResponseFormat)
	}
	if !req.ResponseFormat.JSONSchema.Strict {
		t.Error("the schema does not bind, so the provider may ignore it")
	}
	if req.ResponseFormat.JSONSchema.Name != "submit_preflight" {
		t.Errorf("schema name = %q", req.ResponseFormat.JSONSchema.Name)
	}
	// Both must go, or the provider sees two ways to answer.
	if req.Tools != nil || req.ToolChoice != nil {
		t.Errorf("the tool call was left alongside the schema: tools=%v choice=%v", req.Tools, req.ToolChoice)
	}
}

// Many tools is the opposite request — the ReAct loop offering what it can do.
// Converting that would take away the model's choice of which to call, which is
// the entire point of offering them.
func TestAsSchemaRequest_LeavesRealToolCallingAlone(t *testing.T) {
	req := &ChatRequest{
		Tools: []ToolDef{
			{Function: FunctionDef{Name: "bash", Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`)}},
			{Function: FunctionDef{Name: "web_fetch", Parameters: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}}}`)}},
		},
		ToolChoice: "required",
	}
	if asSchemaRequest(req, "openai") != nil {
		t.Error("a multi-tool call was converted; the model can no longer choose")
	}
	if len(req.Tools) != 2 {
		t.Error("the tools were taken away")
	}
}

// "auto" means the model may answer without calling anything. A schema would
// force it to.
func TestAsSchemaRequest_LeavesAnOptionalCallAlone(t *testing.T) {
	req := oneTool(`{"type":"object","properties":{"a":{"type":"string"}}}`)
	req.ToolChoice = "auto"
	if asSchemaRequest(req, "openai") != nil {
		t.Error("an optional tool call was forced into a schema")
	}
}

// Anthropic constrains tool input itself and speaks a different shape.
func TestAsSchemaRequest_LeavesAnthropicAlone(t *testing.T) {
	req := oneTool(`{"type":"object","properties":{"a":{"type":"string"}}}`)
	if asSchemaRequest(req, ProviderAnthropic) != nil {
		t.Error("an Anthropic request was rewritten")
	}
}

// A strict schema must be closed: a decoder walking it needs to know when the
// object is finished, and an open one never is.
func TestClosedSchema_ClosesTheObjectAndItsNesting(t *testing.T) {
	raw := `{"type":"object","properties":{
	   "mode":{"type":"string"},
	   "context":{"type":"object","properties":{"intent":{"type":"string"},"urls":{"type":"array","items":{"type":"string"}}}}
	 },"required":["mode"]}`
	var m map[string]any
	if err := json.Unmarshal(closedSchema(json.RawMessage(raw)), &m); err != nil {
		t.Fatalf("the closed schema is not JSON: %v", err)
	}
	if m["additionalProperties"] != false {
		t.Error("the top-level object is still open")
	}
	req, _ := m["required"].([]any)
	if len(req) != 2 {
		t.Errorf("required lists %d of 2 properties; a decoder cannot tell when the reply ends", len(req))
	}
	ctx := m["properties"].(map[string]any)["context"].(map[string]any)
	if ctx["additionalProperties"] != false {
		t.Error("the nested object is still open")
	}
}

// Twice the same, so a request that differs only in map order does not defeat a
// provider's prompt cache.
func TestClosedSchema_IsStable(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"z":{"type":"string"},"a":{"type":"string"},"m":{"type":"string"}}}`)
	first := string(closedSchema(raw))
	for i := 0; i < 8; i++ {
		if got := string(closedSchema(raw)); got != first {
			t.Fatalf("the schema marshals differently between calls:\n %s\n %s", first, got)
		}
	}
	if !strings.Contains(first, `["a","m","z"]`) {
		t.Errorf("required is not sorted: %s", first)
	}
}

// Not an object schema — nothing to close, so the call is left as it was rather
// than sent in a shape the provider will reject.
func TestClosedSchema_RefusesWhatItCannotClose(t *testing.T) {
	for _, raw := range []string{`{"type":"string"}`, `{"type":"object"}`, `not json`} {
		if closedSchema(json.RawMessage(raw)) != nil {
			t.Errorf("%q was closed", raw)
		}
	}
}

// The stage above asked for a tool call and reads ToolCalls[0]. A schema reply
// arrives as content, so it is put back into the shape the caller waits for —
// which is what keeps every call site unchanged.
func TestAsToolReply_PutsTheReplyBackInShape(t *testing.T) {
	tool := &ToolDef{Function: FunctionDef{Name: "submit_decision"}}
	resp := &ChatResponse{Choices: []Choice{{Message: Message{Content: `{"decision":"conclude"}`}}}}

	asToolReply(resp, tool)

	tc := resp.Choices[0].Message.ToolCalls
	if len(tc) != 1 {
		t.Fatalf("the caller sees %d tool calls; it reads ToolCalls[0]", len(tc))
	}
	if tc[0].Function.Name != "submit_decision" || tc[0].Function.Arguments != `{"decision":"conclude"}` {
		t.Errorf("the reply was not carried across: %+v", tc[0].Function)
	}
	if resp.Choices[0].Message.Content != "" {
		t.Error("the content was left as well, so the reply is there twice")
	}
}

// A provider that answers with a real tool call is left alone — including one
// that quietly stops honouring the schema request.
func TestAsToolReply_LeavesARealToolCallAlone(t *testing.T) {
	tool := &ToolDef{Function: FunctionDef{Name: "submit_decision"}}
	resp := &ChatResponse{Choices: []Choice{{Message: Message{
		ToolCalls: []ToolCall{{Function: FunctionCall{Name: "submit_decision", Arguments: `{"a":1}`}}},
	}}}}
	asToolReply(resp, tool)
	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Error("a real tool call was disturbed")
	}
}
