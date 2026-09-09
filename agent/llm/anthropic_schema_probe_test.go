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
	if replaced := asSchemaRequest(req, ProviderAnthropic); replaced != nil {
		t.Fatal("an Anthropic model was moved onto the schema wire")
	}
	if req.ResponseFormat != nil {
		t.Error("a schema request was built for Anthropic")
	}
	if req.Tools == nil {
		t.Error("the tool call was taken away")
	}
}

// Every other label gets the schema wire — including a model reached through an
// aggregator, whose provider says who serves the request and not which wire the
// model behind it can take.
//
// What that wire then asks for is a contradiction: strict mode requires every
// object to declare its properties and forbid the rest, and params declares
// none and permits everything. A provider resolving that by emitting {} gives
// every step empty params, which is a plan that names a tool and supplies it
// nothing.
func TestAsSchemaRequest_StrictCannotCarryFreeFormParams(t *testing.T) {
	req := planLikeRequest()
	if replaced := asSchemaRequest(req, ProviderOpenAI); replaced == nil {
		t.Fatal("the request was not converted, so there is no schema to inspect")
	}
	if !req.ResponseFormat.JSONSchema.Strict {
		t.Fatal("the schema does not bind")
	}

	var got map[string]any
	if err := json.Unmarshal(req.ResponseFormat.JSONSchema.Schema, &got); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	steps := got["properties"].(map[string]any)["steps"].(map[string]any)
	item := steps["items"].(map[string]any)
	params := item["properties"].(map[string]any)["params"].(map[string]any)

	if params["additionalProperties"] != true {
		t.Errorf("params additionalProperties = %v; the free-form object was closed", params["additionalProperties"])
	}
	if _, declared := params["properties"]; declared {
		t.Error("params gained properties it does not have")
	}
	t.Logf("strict=%v, params=%v — an object declaring nothing and permitting everything, inside a schema that binds",
		req.ResponseFormat.JSONSchema.Strict, params)
}
