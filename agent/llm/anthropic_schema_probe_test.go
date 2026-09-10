package llm

import (
	"encoding/json"
	"testing"
)

// The plan tool's params, as the executive declares them: an object with no
// declared properties, taking whatever the named tool's signature needs.
const freeFormParams = `{
  "type": "object",
  "properties": {
    "steps": {"type":"array","items":{"type":"object",
      "required":["tool","params","tag"],
      "properties":{
        "tool":{"type":"string"},
        "tag":{"type":"string"},
        "params":{"type":"object","additionalProperties":true}
      }}}
  }
}`

func planLikeRequest() *ChatRequest {
	return &ChatRequest{
		Tools: []ToolDef{{Type: "function", Function: FunctionDef{
			Name: "plan", Parameters: json.RawMessage(freeFormParams)}}},
		ToolChoice: ForceToolChoice("plan"),
	}
}

// An Anthropic model keeps the tool wire. The guard exists because the strict
// schema below cannot carry a free-form object.
func TestAsSchemaRequest_AnthropicKeepsTheToolWire(t *testing.T) {
	req := planLikeRequest()
	if replaced := asSchemaRequest(req, ProviderAnthropic, "claude-sonnet-4.6"); replaced != nil {
		t.Fatal("an Anthropic model was moved onto the schema wire")
	}
	if req.ResponseFormat != nil {
		t.Error("a schema request was built for Anthropic")
	}
	if req.Tools == nil {
		t.Error("the tool call was taken away")
	}
}

// A schema strict cannot carry is not sent as one.
//
// This test used to assert the opposite, and existed to record a contradiction:
// strict mode requires every object to declare its properties and forbid the
// rest, params declares none and permits everything, and the request went out
// saying strict anyway. Both ways that resolves are invisible from the reply —
// a provider that enforces refuses the call, and one that does not accepts it
// and answers unconstrained.
//
// Anthropic resolved it a third way, by honouring the empty schema literally
// and answering the forced call with empty params: "required parameter command
// is not supplied", three corrections, a dead run.
//
// So the call now stays on tool calling, which is the wire that can express it.
func TestAsSchemaRequest_DeclinesASchemaStrictCannotCarry(t *testing.T) {
	req := planLikeRequest()
	if replaced := asSchemaRequest(req, ProviderOpenAI, "gpt-4o"); replaced != nil {
		t.Fatal("a schema with a free-form params object was converted anyway")
	}
	if req.ResponseFormat != nil {
		t.Error("the request was labelled strict for a schema that cannot be enforced")
	}
	if req.Tools == nil || req.ToolChoice == nil {
		t.Error("the forced tool call was taken away, leaving the stage no way to run")
	}
}

// A schema that CAN be carried still is. The guard refuses what strict cannot
// express; it must not refuse everything.
func TestAsSchemaRequest_StillConvertsWhatStrictCanCarry(t *testing.T) {
	closeable := `{"type":"object","properties":{"verdict":{"type":"string"}},"required":["verdict"]}`
	req := &ChatRequest{
		Tools: []ToolDef{{Type: "function", Function: FunctionDef{
			Name: "judge", Parameters: json.RawMessage(closeable)}}},
		ToolChoice: ForceToolChoice("judge"),
	}
	replaced := asSchemaRequest(req, ProviderOpenAI, "gpt-4o")
	if replaced == nil {
		t.Fatal("a schema strict can carry was left on tool calling")
	}
	if req.ResponseFormat == nil || !req.ResponseFormat.JSONSchema.Strict {
		t.Fatal("the converted request does not bind")
	}
	if p := StrictProblems(req.ResponseFormat.JSONSchema.Schema); len(p) > 0 {
		t.Errorf("a request went out labelled strict with %d problem(s): %s", len(p), p[0])
	}
}
