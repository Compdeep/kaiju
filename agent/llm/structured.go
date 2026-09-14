package llm

import (
	"encoding/json"
	"log"
	"strings"
)

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
 *
 *       "Made to call it" has two spellings. tool_choice "required" is one, and
 *       with a single tool bound it can mean nothing else. Naming that same tool
 *       — ForceToolChoice — is the other, and it says MORE, not less: call this
 *       one, by name. The executive spells it that way, to stop a weak model
 *       answering the plan request with a direct web_search call.
 *
 *       Reading only the string missed it, so the stage with the largest schema
 *       in the engine — the one every other stage plans from — was the only one
 *       left on unenforced tool calling. Nobody decided that; it fell out of a
 *       type assertion.
 * param: req - the outgoing request.
 * return: true when the request is a schema in tool clothing.
 */
func asksForOneShape(req *ChatRequest) bool {
	if req == nil || len(req.Tools) != 1 || req.ResponseFormat != nil {
		return false
	}
	if choice, ok := req.ToolChoice.(string); ok {
		return choice == "required"
	}
	// A pin naming some OTHER tool is not this request's shape, so match the
	// name rather than accepting any forced call.
	return forcedToolName(req.ToolChoice) == req.Tools[0].Function.Name
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
/*
 * speaksAnthropic reports whether the model behind a request is Anthropic's,
 * whoever is carrying the request.
 * desc: The provider says who serves a call. It used to say which wire the
 *       model wants too, back when those were the same fact — a client pointed
 *       at api.anthropic.com served Anthropic models and nothing else.
 *
 *       An aggregator separates them. OpenRouter serves the request and
 *       Anthropic answers it, and only the model id knows which. The guard
 *       below is keyed on the provider, so an Anthropic model reached that way
 *       was put on the wire the guard exists to keep it off — and its replies
 *       came back as prose, deterministically, six times out of six.
 *
 *       Matched on the id's vendor prefix, which every id on that route carries
 *       ("anthropic/claude-opus-4.8"), and on "claude" for a deployment that
 *       names the model without one.
 * param: model - the model the request will run on.
 * return: true when it is Anthropic's, however it is reached.
 */
func speaksAnthropic(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(m, "anthropic/") || strings.Contains(m, "claude")
}

func asSchemaRequest(req *ChatRequest, provider, model string) *ToolDef {
	// Anthropic constrains tool input on its own and speaks a different shape —
	// see completeAnthropic. Nothing to rewrite there, and that holds however
	// the request reaches it: the provider names the carrier, the model names
	// the wire.
	//
	// model is passed in rather than read off the request because the request
	// may not carry one yet — completeOpenAI fills it from the client's default
	// afterwards, so a caller relying on that default would look like a model
	// with no name here.
	if provider == ProviderAnthropic || !asksForOneShape(req) {
		return nil
	}
	if speaksAnthropic(model) {
		return nil
	}
	tool := req.Tools[0]
	schema := closedSchema(tool.Function.Parameters)
	if schema == nil {
		return nil // not an object schema; leave the call as it was
	}

	// Closed is not the same as closeable. What is left after closing is asked
	// of the checker, which walks the same document by the same route, and a
	// schema that still breaks a rule is not sent as one.
	//
	// Both outcomes of sending it are bad and neither is visible from the reply:
	// a provider that enforces refuses the request, and the client reads that
	// refusal as a missing capability and stops offering schemas to the model
	// for the rest of the process; one that does not enforce accepts it and
	// answers unconstrained, which looks exactly like enforcement working.
	//
	// This is also what keeps the fault from recurring. Whatever shape is added
	// next, a document that cannot carry strict is never labelled strict — the
	// call stays on tool calling, deliberately, and says which stage and why.
	if problems := StrictProblems(schema); len(problems) > 0 {
		log.Printf("[schema] %s stays on tool calling: strict cannot carry it (%s)",
			tool.Function.Name, problems[0])
		return nil
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
	// The reason travels with the call. A caller does not read ToolCalls on its
	// own — it asks whether a tool was called at all, and on this wire the
	// provider says "stop" because no tool was offered. Moving the arguments and
	// leaving the reason behind makes a reply that carries a tool call and
	// denies having one, which every such caller reads as prose.
	//
	// "length" is left alone: that reply was cut off mid-JSON, and a stage that
	// asks for a shorter plan on seeing it must keep seeing it. Rewriting it
	// here would hand the fragment to the parser as a whole plan.
	if c.FinishReason != "length" {
		c.FinishReason = "tool_calls"
	}
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
	if !declaresProperties(m) {
		return nil
	}

	// Every node the document reaches, through every kind of link — see
	// eachSchemaNode. This used to follow properties and items.properties only,
	// so a shape nesting through anyOf was reached at the array above it and
	// left untouched below.
	eachSchemaNode(m, "", closeOne)

	out, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return out
}

/*
 * closeOne makes one node acceptable to a strict decoder, where it can be.
 * desc: Two rules, both the provider's: an object must forbid keys it did not
 *       declare, and must require every key it did.
 *
 *       An object that declares no keys is left exactly as it is. Closing it
 *       would permit nothing, so {} becomes its only legal value — a stricter
 *       schema than anyone asked for, and a useless one. Strict has no way to
 *       express "keys I cannot name in advance", so this is not a thing to
 *       repair here: StrictProblems reports it, and asSchemaRequest declines to
 *       claim strict for the document at all.
 * param: m - the node, modified in place.
 */
// outsideStrict names the keywords a strict schema has no use for.
//
// Two reasons a keyword lands here, and they are different reasons. Some state
// a rule about the FINISHED document — if/then/else, and the allOf that holds
// them — which a filter built before the first token cannot act on, because it
// answers "what may this document contain" and the filter only ever asks "what
// may come next". The rest constrain a value's CONTENT rather than the shape
// around it — a string's length or pattern, a number's range — and shape is all
// a filter carries.
//
// Dropping them here loses nothing, because the provider could not have applied
// them either way, and it is dropped from the SENT copy alone: the tool's own
// schema is untouched, and dispatcher_validation.go still reads every one of
// these off it and enforces them against the finished arguments. That split is
// the point. The two readers do not have the same powers, and a schema written
// for the one that reads whole documents must not disable the one that reads
// tokens.
//
// Sending one instead was not a small loss. A provider that checks refuses the
// request; one that does not accepts it, builds no filter, and answers
// unconstrained — and nothing in the reply tells the two apart. Five tools
// carried an allOf/if/then written for the validator, and from the day the plan
// schema began embedding tool schemas no plan was constrained at all.
var outsideStrict = []string{
	"allOf", "if", "then", "else", "not", "oneOf",
	"dependentSchemas", "dependentRequired", "dependencies",
	"patternProperties", "propertyNames", "unevaluatedProperties",
	"minLength", "maxLength", "pattern",
	"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
	"minItems", "maxItems", "uniqueItems", "contains", "minContains", "maxContains",
	"format", "default",
}

// dropOutsideStrict removes from one node the keywords a strict schema has no
// use for. Called on every node the walk reaches, so a keyword nested inside a
// branch is removed as surely as one at the root.
func dropOutsideStrict(m map[string]any) {
	for _, k := range outsideStrict {
		delete(m, k)
	}
}

func closeOne(_ string, m map[string]any) {
	dropOutsideStrict(m)
	if !declaresProperties(m) {
		return
	}
	props := m["properties"].(map[string]any)
	m["additionalProperties"] = false

	// Strict requires every declared key in required, so an optional field has
	// to become required. A field that is required and has nothing to say needs
	// a value it may legally take, so its type gains null — which is how strict
	// writes an optional field, and what the checker's own message says to do.
	//
	// Widening rather than compelling. Without it, closing a schema quietly made
	// every optional field mandatory with no empty value available, and the
	// model had to invent one: plan.answer is required today for that reason,
	// which is a good part of why it arrives holding prose.
	was := requiredSet(m["required"])
	for k, v := range props {
		if !was[k] {
			nullable(v)
		}
	}
	m["required"] = keysOf(props)
}

/*
 * nullable lets a node hold null as well as what it already held.
 * desc: Applied to a field that was optional and is being made required.
 *
 *       An enum is widened with it, because a value outside the enum is not
 *       legal however the type reads — a field typed string-or-null whose enum
 *       lists neither can hold nothing at all, and the schema is unsatisfiable.
 *
 *       Left alone where widening would guess: a const says exactly one value
 *       and null is not it, and a type this does not recognise is not something
 *       to edit. What is left over is reported by the checker rather than
 *       repaired blindly.
 * param: node - the field's schema, modified in place.
 */
func nullable(node any) {
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	if _, pinned := m["const"]; pinned {
		return
	}

	switch t := m["type"].(type) {
	case string:
		if t == "null" {
			return
		}
		m["type"] = []any{t, "null"}
	case []any:
		for _, x := range t {
			if s, _ := x.(string); s == "null" {
				return
			}
		}
		m["type"] = append(t, "null")
	default:
		return // no type to widen; leave it for the checker to report
	}

	if enum, ok := m["enum"].([]any); ok {
		for _, v := range enum {
			if v == nil {
				return
			}
		}
		m["enum"] = append(enum, nil)
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

/*
 * asToolRequestAgain puts a converted request back the way the caller wrote it.
 * desc: Not every model behind an OpenAI-compatible endpoint implements
 *       response_format. Kimi-K2 through Novita answers "model features
 *       structured outputs not support" with a 400, and a stage whose request
 *       was rewritten here would fail on a model it used to work on — the
 *       rewrite is an optimisation, and an optimisation must not take
 *       capability away.
 *
 *       So the 400 is read, not predicted. A capability list would have to be
 *       maintained against every model on every provider, and would be wrong
 *       the day either changes; the provider already knows the answer and says
 *       so. One retry, on the wire the caller originally asked for.
 * param: req - the rewritten request, restored in place.
 * param: tool - the tool that was replaced, from asSchemaRequest.
 */
func asToolRequestAgain(req *ChatRequest, tool *ToolDef) {
	if req == nil || tool == nil {
		return
	}
	req.ResponseFormat = nil
	req.Tools = []ToolDef{*tool}
	req.ToolChoice = ForceToolChoice(tool.Function.Name)
}

/*
 * ignoredTheSchema reports a reply that came back as prose from a call that
 * forced a shape.
 * desc: The rewrite is meant to be enforced. A provider that cannot enforce it
 *       usually says so with a 400 — rejectsSchemas reads that, and the caller
 *       puts the request back as a tool call and sends it again.
 *
 *       Not every provider says so. An Anthropic model reached through an
 *       aggregator that emulates json_schema answered 200 with markdown, six
 *       times out of six on the same request. Nothing errored, so the retry
 *       never fired, and the prose travelled on as a successful reply — the
 *       executive read no plan in it, reported a conversational answer, and the
 *       run ended with no result at all.
 *
 *       So the test is the reply, not the status: a stage that forced one shape
 *       and got sentences did not get what it asked for, whoever says otherwise.
 *
 *       Deliberately narrow. Only a reply with no tool call AND content that
 *       does not begin as JSON counts — a provider answering correctly on the
 *       schema wire returns the document as content, and that must not be
 *       mistaken for prose.
 * param: resp - what came back.
 * return: true when the shape was asked for and not honoured.
 */
func ignoredTheSchema(resp *ChatResponse) bool {
	if resp == nil || len(resp.Choices) == 0 {
		return false
	}
	c := resp.Choices[0]
	if len(c.Message.ToolCalls) > 0 {
		return false
	}
	body := strings.TrimSpace(c.Message.Content)
	if body == "" {
		return false // an empty reply is its own fault, and has its own handling
	}
	return !strings.HasPrefix(body, "{") && !strings.HasPrefix(body, "[")
}

// rejectsSchemas reports whether an error is the provider saying this model has
// no structured-output support — as opposed to a schema this model would accept
// but the request got wrong, which a retry would not fix.
func rejectsSchemas(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "400") {
		return false
	}
	return strings.Contains(msg, "structured output") ||
		strings.Contains(msg, "response_format") ||
		strings.Contains(msg, "json_schema")
}
