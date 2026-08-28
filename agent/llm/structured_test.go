package llm

import (
	"encoding/json"
	"errors"
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

// The executive pins the model to `plan` by name rather than saying "required",
// so a weak reasoning model cannot answer a plan request with a direct
// web_search call. That is a STRONGER demand than "required", and reading only
// the string form missed it — leaving the largest schema in the engine, the one
// every other stage plans from, on the wire that does not enforce.
func TestAsSchemaRequest_ConvertsAToolPinnedByName(t *testing.T) {
	req := oneTool(`{"type":"object","properties":{"steps":{"type":"array","items":{"type":"object","properties":{"tool":{"type":"string"}}}}},"required":["steps"]}`)
	req.Tools[0].Function.Name = "plan"
	req.ToolChoice = ForceToolChoice("plan")

	replaced := asSchemaRequest(req, "openai")
	if replaced == nil {
		t.Fatal("the planner was left on the advisory wire; a pinned call is still a request for one shape")
	}
	if req.ResponseFormat == nil || req.ResponseFormat.JSONSchema.Name != "plan" {
		t.Fatalf("no plan schema was built: %+v", req.ResponseFormat)
	}
	if req.Tools != nil || req.ToolChoice != nil {
		t.Errorf("the pin was left beside the schema: tools=%v choice=%v", req.Tools, req.ToolChoice)
	}
}

// A pin naming a DIFFERENT tool than the one bound is not this request's shape.
// Matching the name rather than accepting any forced call is what keeps a
// mismatched request on the wire it was written for.
func TestAsSchemaRequest_LeavesAPinForAnotherToolAlone(t *testing.T) {
	req := oneTool(`{"type":"object","properties":{"a":{"type":"string"}}}`)
	req.ToolChoice = ForceToolChoice("some_other_tool")
	if asSchemaRequest(req, "openai") != nil {
		t.Error("a request pinned to a tool it does not carry was rewritten")
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
	tool := &ToolDef{Function: FunctionDef{Name: "reflector_decision"}}
	resp := &ChatResponse{Choices: []Choice{{Message: Message{Content: `{"decision":"conclude"}`}}}}

	asToolReply(resp, tool)

	tc := resp.Choices[0].Message.ToolCalls
	if len(tc) != 1 {
		t.Fatalf("the caller sees %d tool calls; it reads ToolCalls[0]", len(tc))
	}
	if tc[0].Function.Name != "reflector_decision" || tc[0].Function.Arguments != `{"decision":"conclude"}` {
		t.Errorf("the reply was not carried across: %+v", tc[0].Function)
	}
	if resp.Choices[0].Message.Content != "" {
		t.Error("the content was left as well, so the reply is there twice")
	}
}

// A provider that answers with a real tool call is left alone — including one
// that quietly stops honouring the schema request.
func TestAsToolReply_LeavesARealToolCallAlone(t *testing.T) {
	tool := &ToolDef{Function: FunctionDef{Name: "reflector_decision"}}
	resp := &ChatResponse{Choices: []Choice{{Message: Message{
		ToolCalls: []ToolCall{{Function: FunctionCall{Name: "reflector_decision", Arguments: `{"a":1}`}}},
	}}}}
	asToolReply(resp, tool)
	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Error("a real tool call was disturbed")
	}
}

// A model with no structured-output support rejects the rewrite outright. The
// rewrite buys enforcement; it must not cost a stage the ability to run at all,
// so the request goes back the way the caller wrote it and is sent once more.
//
// Measured: kimi-k2 through Novita answers `{"code":400, "reason":
// "INVALID_REQUEST_BODY", "message":"model features structured outputs not
// support"}` — 6 attempts, 6 rejections. Without this the planner hard-failed on
// a model it had been planning on.
func TestRejectsSchemas_ReadsTheProvidersAnswer(t *testing.T) {
	yes := []error{
		errors.New(`HTTP 400: {"message":"model features structured outputs not support"}`),
		errors.New(`HTTP 400: response_format is not supported for this model`),
		errors.New(`http 400: json_schema unsupported`),
	}
	for _, e := range yes {
		if !rejectsSchemas(e) {
			t.Errorf("not read as a capability rejection, so the stage fails instead of retrying: %v", e)
		}
	}
	no := []error{
		nil,
		errors.New("HTTP 429: rate limited"),
		errors.New("HTTP 500: internal error"),
		// A schema this model WOULD accept, got wrong. Retrying the same
		// request on the tool wire is not the fix for it.
		errors.New(`HTTP 400: invalid_request_error: messages must not be empty`),
	}
	for _, e := range no {
		if rejectsSchemas(e) {
			t.Errorf("read as a capability rejection, so a real error is retried and hidden: %v", e)
		}
	}
}

// The restored request must be the one the caller wrote — the same single tool,
// pinned by name — or the retry asks for something the stage cannot parse.
func TestAsToolRequestAgain_RestoresWhatTheCallerWrote(t *testing.T) {
	req := oneTool(`{"type":"object","properties":{"steps":{"type":"array"}}}`)
	req.Tools[0].Function.Name = "plan"
	req.ToolChoice = ForceToolChoice("plan")

	replaced := asSchemaRequest(req, "openai")
	if replaced == nil {
		t.Fatal("the request did not convert, so there is nothing to restore")
	}
	asToolRequestAgain(req, replaced)

	if req.ResponseFormat != nil {
		t.Error("the schema was left on the retry; the provider that just refused it sees it again")
	}
	if len(req.Tools) != 1 || req.Tools[0].Function.Name != "plan" {
		t.Fatalf("the tool was not restored: %+v", req.Tools)
	}
	if forcedToolName(req.ToolChoice) != "plan" {
		t.Errorf("the pin was not restored: %v", req.ToolChoice)
	}
}

// The caller reads FinishReason to decide whether a tool was called at all.
// A schema reply comes back "stop", because on the wire no tool was offered —
// so moving the content into ToolCalls and leaving the reason alone puts the
// reply in a shape no caller recognises: it has a tool call and says it does
// not.
//
// The executive reads exactly that (`FinishReason == "tool_calls" && len(...)`)
// and it is the stage every other stage plans from. Left unrestored, a plan
// that parsed and validated was read as prose, the run fell to the chat lane,
// and the user was told a file had been written that no step ever wrote.
func TestAsToolReply_RestoresTheFinishReason(t *testing.T) {
	req := oneTool(`{"type":"object","properties":{"steps":{"type":"array"}}}`)
	req.Tools[0].Function.Name = "plan"
	req.ToolChoice = ForceToolChoice("plan")
	replaced := asSchemaRequest(req, "openai")
	if replaced == nil {
		t.Fatal("the request did not convert, so there is no reply to restore")
	}

	resp := &ChatResponse{Choices: []Choice{{
		Message:      Message{Content: `{"steps":[{"tool":"file_write","tag":"w","params":{"path":"/tmp/uchan"}}]}`},
		FinishReason: "stop",
	}}}
	asToolReply(resp, replaced)

	if got := resp.Choices[0].FinishReason; got != "tool_calls" {
		t.Errorf("finish reason %q: a caller gated on \"tool_calls\" skips the plan it was just handed", got)
	}
}

// A reply the provider genuinely cut short must keep saying so. The executive
// asks for a shorter plan on "length", and rewriting that to "tool_calls" would
// hand a truncated plan to the parser instead.
func TestAsToolReply_LeavesACutReplyCut(t *testing.T) {
	req := oneTool(`{"type":"object","properties":{"steps":{"type":"array"}}}`)
	replaced := asSchemaRequest(req, "openai")
	resp := &ChatResponse{Choices: []Choice{{
		Message:      Message{Content: `{"steps":[{"tool":"file_wr`},
		FinishReason: "length",
	}}}
	asToolReply(resp, replaced)

	if got := resp.Choices[0].FinishReason; got != "length" {
		t.Errorf("finish reason %q: a cut reply must stay cut, or it is parsed as a whole plan", got)
	}
}
