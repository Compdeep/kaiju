package llm

import "encoding/json"

// Asking the provider to hold the model to the schema.
//
// A stage that wants one shape back declares it as a tool and forces the call:
// one ToolDef, tool_choice "required". Every reasoning stage in this engine does
// that, and the schemas are exact.
//
// The provider does not have to honour them. Tool arguments are generated as
// ordinary text and labelled afterwards, so "the model called your tool" says
// nothing about whether what it wrote parses. Measured against qwen3-30b through
// OpenRouter, on the preflight schema, forty times: the reply was labelled a
// tool call and the arguments were pretty-printed JSON with a misplaced quote —
// after which the model could not recover and emitted whitespace until it hit
// the ceiling. Thirty-six seconds, spent on one wrong character.
//
//	tools + tool_choice "required"    0/3 valid    2048 tokens
//	tools + function "strict": true   0/3 valid    2048 tokens   (silently dropped)
//	response_format json_schema       3/3 valid     115 tokens
//
// response_format is a different surface, and it is the one that is enforced:
// the provider walks the schema as the model generates and refuses any token the
// grammar does not permit. A misplaced quote is not merely unlikely there, it is
// unreachable.
//
// So the request is rewritten here, at the one place every call passes through,
// and the reply is rewritten back into the shape the caller declared. Nothing
// above this file changes: a stage still declares a tool, still reads
// ToolCalls[0].Function.Arguments, and never learns which wire carried it.

// ResponseFormat asks for a reply that conforms to a schema.
type ResponseFormat struct {
	Type       string      `json:"type"`                  // "json_schema"
	JSONSchema *JSONSchema `json:"json_schema,omitempty"` // when Type is json_schema
}

// JSONSchema names a schema and says whether it binds.
type JSONSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

/*
 * asksForOneShape reports whether a request is a stage asking for a shape rather
 * than a stage offering tools.
 * desc: One tool, and the model made to call it, is how every reasoning stage in
 *       this engine says "reply in this form". Many tools is the opposite — the
 *       ReAct loop offering what it can do — and converting that would take away
 *       the model's choice of which to call, which is the entire point of it.
 * param: req - the outgoing request.
 * return: true when the request is a schema in tool clothing.
 */
func asksForOneShape(req *ChatRequest) bool {
	if req == nil || len(req.Tools) != 1 || req.ResponseFormat != nil {
		return false
	}
	choice, _ := req.ToolChoice.(string)
	return choice == "required"
}

/*
 * asSchemaRequest rewrites a forced single-tool call into a schema request.
 * desc: Returns the tool it replaced, so the reply can be put back into the
 *       shape the caller is waiting for. Returns nil when the request is not one
 *       of these, or when the provider would not honour it.
 *
 *       additionalProperties is set false and every declared property required,
 *       because a strict schema must be closed — a provider that constrains
 *       generation needs to know when the object is finished, and an open one
 *       never is.
 * param: req - the request, modified in place when it converts.
 * param: provider - which API this is going to.
 * return: the tool that was replaced, or nil when nothing was changed.
 */
func asSchemaRequest(req *ChatRequest, provider string) *ToolDef {
	// Anthropic constrains tool input on its own and speaks a different shape —
	// see completeAnthropic. Nothing to rewrite there.
	if provider == ProviderAnthropic || !asksForOneShape(req) {
		return nil
	}
	tool := req.Tools[0]
	schema := closedSchema(tool.Function.Parameters)
	if schema == nil {
		return nil // not an object schema; leave the call as it was
	}
	replaced := tool
	req.ResponseFormat = &ResponseFormat{
		Type: "json_schema",
		JSONSchema: &JSONSchema{
			Name:   tool.Function.Name,
			Strict: true,
			Schema: schema,
		},
	}
	req.Tools = nil
	req.ToolChoice = nil
	return &replaced
}

/*
 * asToolReply puts a schema reply back into the shape the caller declared.
 * desc: The stage above asked for a tool call and reads
 *       ToolCalls[0].Function.Arguments. A schema reply arrives as content, so
 *       it is moved. Callers are left unchanged, and a provider that quietly
 *       stops honouring response_format still works: the content is simply
 *       whatever it sent, and the caller's own parser reports it.
 * param: resp - the reply, modified in place.
 * param: tool - the tool that was replaced, from asSchemaRequest.
 */
func asToolReply(resp *ChatResponse, tool *ToolDef) {
	if resp == nil || tool == nil || len(resp.Choices) == 0 {
		return
	}
	c := &resp.Choices[0]
	if len(c.Message.ToolCalls) > 0 || c.Message.Content == "" {
		return
	}
	c.Message.ToolCalls = []ToolCall{{
		ID:       "call_" + tool.Function.Name,
		Type:     "function",
		Function: FunctionCall{Name: tool.Function.Name, Arguments: c.Message.Content},
	}}
	c.Message.Content = ""
}

/*
 * closedSchema returns an object schema a strict decoder can finish.
 * desc: Strict mode requires additionalProperties false and every property
 *       listed as required — an open object has no end, so a grammar cannot say
 *       when the reply is complete. The stage's own optional fields are not lost
 *       by this: a required field the model has nothing to put in comes back
 *       empty, which every parser here already treats as absent.
 * param: raw - the tool's parameter schema.
 * return: the closed schema, or nil when it is not an object.
 */
func closedSchema(raw json.RawMessage) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	if t, _ := m["type"].(string); t != "object" {
		return nil
	}
	props, ok := m["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return nil
	}
	m["additionalProperties"] = false
	required := make([]string, 0, len(props))
	for k := range props {
		required = append(required, k)
	}
	// Sorted, so the same schema marshals the same way twice — a request that
	// differs only in map order defeats a provider's prompt cache.
	for i := 1; i < len(required); i++ {
		for j := i; j > 0 && required[j] < required[j-1]; j-- {
			required[j], required[j-1] = required[j-1], required[j]
		}
	}
	m["required"] = required
	closeNested(props)
	out, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return out
}

// closeNested applies the same closure to nested object schemas, which a strict
// decoder walks the same way as the top level.
func closeNested(props map[string]any) {
	for _, v := range props {
		p, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if items, ok := p["items"].(map[string]any); ok {
			if inner, ok := items["properties"].(map[string]any); ok {
				items["additionalProperties"] = false
				items["required"] = keysOf(inner)
				closeNested(inner)
			}
		}
		inner, ok := p["properties"].(map[string]any)
		if !ok {
			continue
		}
		p["additionalProperties"] = false
		p["required"] = keysOf(inner)
		closeNested(inner)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
