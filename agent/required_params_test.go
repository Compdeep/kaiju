package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A schema that both requires a parameter AND allows unlisted ones. This is the
// shape the old code could not police: it returned early on
// additionalProperties, so "required" was never read for any open-schema tool.
const openSchemaWithRequired = `{
	"type": "object",
	"properties": {
		"goal": {"type": "string"},
		"mode": {"type": "string"}
	},
	"required": ["goal", "mode"]
}`

const closedSchemaWithRequired = `{
	"type": "object",
	"properties": {
		"query": {"type": "string"},
		"max_results": {"type": "integer"}
	},
	"required": ["query"],
	"additionalProperties": false
}`

// ── parseToolSchema reads required ───────────────────────────────────────

func TestParseToolSchema_ReadsRequiredInDeclaredOrder(t *testing.T) {
	s, err := parseToolSchema(json.RawMessage(openSchemaWithRequired))
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got := strings.Join(s.Required, ","); got != "goal,mode" {
		t.Fatalf("required should keep the schema's own order, got %q", got)
	}
}

func TestParseToolSchema_AbsentRequiredIsEmpty(t *testing.T) {
	s, _ := parseToolSchema(json.RawMessage(`{"properties":{"a":{}}}`))
	if len(s.Required) != 0 {
		t.Fatalf("a schema with no required list should yield none, got %v", s.Required)
	}
}

// ── missingRequiredParams ────────────────────────────────────────────────

func TestMissingRequiredParams(t *testing.T) {
	schema, _ := parseToolSchema(json.RawMessage(openSchemaWithRequired))

	cases := []struct {
		name   string
		params map[string]any
		want   string // comma-joined, "" ⇒ nothing missing
	}{
		{"both supplied", map[string]any{"goal": "count the words", "mode": "shallow"}, ""},
		{"one absent", map[string]any{"goal": "count the words"}, "mode"},
		{"none supplied", map[string]any{}, "goal,mode"},
		{"nil params map", nil, "goal,mode"},
		{"explicit nil value", map[string]any{"goal": "x", "mode": nil}, "mode"},
		{"empty string", map[string]any{"goal": "x", "mode": ""}, "mode"},
		{"whitespace only", map[string]any{"goal": "x", "mode": "   "}, "mode"},
		{"unresolved reference counts as supplied", map[string]any{"goal": "${step.0.output}", "mode": "deep"}, ""},
		{"a wrong name does not satisfy a required one", map[string]any{"query": "x", "mode": "deep"}, "goal"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(names(missingRequiredParams(schema, c.params)), ",")
			if got != c.want {
				t.Fatalf("missing = %q, want %q", got, c.want)
			}
		})
	}
}

// false and 0 are values a caller chose, not absences — reporting them missing
// would make a required boolean impossible to set to false.
func TestMissingRequiredParams_FalseAndZeroAreValues(t *testing.T) {
	schema, _ := parseToolSchema(json.RawMessage(`{"properties":{"recursive":{},"depth":{}},"required":["recursive","depth"]}`))
	got := missingRequiredParams(schema, map[string]any{"recursive": false, "depth": 0})
	if len(got) != 0 {
		t.Fatalf("false and 0 are supplied values, got missing %v", got)
	}
}

// ── validateDirectParams ─────────────────────────────────────────────────

// The regression this whole change exists for: an open schema used to return
// before "required" was ever consulted, so a call missing a required parameter
// reached the tool and ran against whatever default the code held.
func TestValidateDirectParams_RejectsMissingRequired_OnOpenSchema(t *testing.T) {
	tool := &fakeTool{name: "compute", params: json.RawMessage(openSchemaWithRequired)}
	err := validateDirectParams(tool, map[string]any{"goal": "count the words"})
	if err == nil {
		t.Fatal("a call omitting a required parameter must be rejected even when the schema allows extras")
	}
	if !strings.Contains(err.Error(), "mode") {
		t.Fatalf("the error should name the parameter that is missing, got %v", err)
	}
}

func TestValidateDirectParams_RejectsMissingRequired_OnClosedSchema(t *testing.T) {
	tool := &fakeTool{name: "web_search", params: json.RawMessage(closedSchemaWithRequired)}
	if err := validateDirectParams(tool, map[string]any{"max_results": 5}); err == nil {
		t.Fatal("expected rejection when query is absent")
	}
}

func TestValidateDirectParams_StillAllowsExtras_WhenRequiredSatisfied(t *testing.T) {
	tool := &fakeTool{name: "compute", params: json.RawMessage(openSchemaWithRequired)}
	err := validateDirectParams(tool, map[string]any{"goal": "g", "mode": "shallow", "ports_data": "${step.1.output}"})
	if err != nil {
		t.Fatalf("an open schema must keep accepting wired extras: %v", err)
	}
}

// ── validatePlanParams ───────────────────────────────────────────────────

func requiredParamReg() *toolapi.Registry {
	reg := toolapi.NewRegistry()
	reg.Replace(&fakeTool{name: "compute", params: json.RawMessage(openSchemaWithRequired)}, "builtin")
	reg.Replace(&fakeTool{name: "web_search", params: json.RawMessage(closedSchemaWithRequired)}, "builtin")
	return reg
}

func TestValidatePlanParams_FlagsMissingRequired(t *testing.T) {
	errs := validatePlanParams([]PlanStep{
		{Tool: "compute", Params: map[string]any{"goal": "count the words"}},
	}, requiredParamReg())

	if len(errs) != 1 {
		t.Fatalf("expected one error, got %d: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0], `"mode"`) || !strings.Contains(errs[0], "goal, mode") {
		t.Fatalf("the error should name the missing parameter and the full requirement, got %q", errs[0])
	}
}

// A step carrying no parameters at all used to be skipped before any check ran,
// so the largest possible omission was the one case that passed.
func TestValidatePlanParams_FlagsAStepWithNoParamsAtAll(t *testing.T) {
	errs := validatePlanParams([]PlanStep{
		{Tool: "web_search", Params: nil},
	}, requiredParamReg())

	if len(errs) != 1 {
		t.Fatalf("a step supplying nothing must be flagged, got %d: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0], `"query"`) {
		t.Fatalf("expected the missing parameter named, got %q", errs[0])
	}
}

func TestValidatePlanParams_ReportsBothFaultsOnOneStep(t *testing.T) {
	errs := validatePlanParams([]PlanStep{
		{Tool: "web_search", Params: map[string]any{"topn": 5}},
	}, requiredParamReg())

	joined := strings.Join(errs, "\n")
	if !strings.Contains(joined, `"query"`) {
		t.Fatalf("expected the missing required parameter reported: %q", joined)
	}
	if !strings.Contains(joined, `"topn"`) {
		t.Fatalf("expected the invented parameter still reported: %q", joined)
	}
}

func TestValidatePlanParams_CleanWhenSatisfied(t *testing.T) {
	errs := validatePlanParams([]PlanStep{
		{Tool: "compute", Params: map[string]any{"goal": "g", "mode": "deep", "wired": "${step.0.output}"}},
		{Tool: "web_search", Params: map[string]any{"query": "q", "max_results": 3}},
	}, requiredParamReg())

	if len(errs) != 0 {
		t.Fatalf("a satisfied plan must be clean, got %v", errs)
	}
}

// An unregistered tool is another check's business; this one must stay quiet
// rather than report every parameter of a tool it cannot see a schema for.
func TestValidatePlanParams_IgnoresUnknownTool(t *testing.T) {
	errs := validatePlanParams([]PlanStep{
		{Tool: "not_registered", Params: nil},
	}, requiredParamReg())

	if len(errs) != 0 {
		t.Fatalf("an unknown tool is not this check's job, got %v", errs)
	}
}

// ── conditional requirements ─────────────────────────────────────────────

// modeSchema mirrors the shape now carried by clipboard, service, git, archive,
// net_info, network_diag and service_control: a flat required list holding only
// the discriminator, plus allOf rules for what each mode additionally needs.
const modeSchema = `{
	"type": "object",
	"properties": {
		"action":  {"type": "string", "enum": ["read", "write", "start", "list"]},
		"content": {"type": "string"},
		"name":    {"type": "string"},
		"command": {"type": "string"}
	},
	"required": ["action"],
	"allOf": [
		{"if": {"properties": {"action": {"const": "write"}}, "required": ["action"]},
		 "then": {"required": ["content"]}},
		{"if": {"properties": {"action": {"enum": ["write", "start"]}}, "required": ["action"]},
		 "then": {"required": ["name"]}}
	],
	"additionalProperties": false
}`

func TestParseConditional_ReadsConstAndEnumRules(t *testing.T) {
	s, err := parseToolSchema(json.RawMessage(modeSchema))
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(s.Conditional) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(s.Conditional))
	}
	if s.Conditional[0].When != "action" || s.Conditional[0].Equals[0] != "write" {
		t.Fatalf("const rule read wrongly: %+v", s.Conditional[0])
	}
	if strings.Join(s.Conditional[1].Equals, ",") != "write,start" {
		t.Fatalf("enum rule read wrongly: %+v", s.Conditional[1])
	}
}

// Anything allOf can express that this does not model must be skipped, never
// guessed at — a misread rule would reject a call that works.
func TestParseConditional_SkipsShapesItDoesNotModel(t *testing.T) {
	for _, raw := range []string{
		`{"type":"object","allOf":[{"if":{"not":{"properties":{"action":{"const":"list"}}}},"then":{"required":["name"]}}]}`,
		`{"type":"object","allOf":[{"if":{"properties":{"a":{"const":"x"},"b":{"const":"y"}}},"then":{"required":["c"]}}]}`,
		`{"type":"object","allOf":[{"if":{"properties":{"action":{"const":"write"}}},"then":{"properties":{"content":{}}}}]}`,
		`{"type":"object","allOf":[{"minProperties":2}]}`,
	} {
		s, err := parseToolSchema(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
		if len(s.Conditional) != 0 {
			t.Fatalf("should have been skipped, got %+v for %s", s.Conditional, raw)
		}
	}
}

func TestMissingRequiredParams_Conditional(t *testing.T) {
	schema, _ := parseToolSchema(json.RawMessage(modeSchema))

	cases := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"a mode with no extra rule needs nothing more", map[string]any{"action": "read"}, ""},
		{"an unmatched mode stays silent", map[string]any{"action": "list"}, ""},
		{"write needs content and name", map[string]any{"action": "write"}, "content,name"},
		{"write with both is clean", map[string]any{"action": "write", "content": "c", "name": "n"}, ""},
		{"start needs only name", map[string]any{"action": "start"}, "name"},
		{"start with name is clean", map[string]any{"action": "start", "name": "n"}, ""},
		{"the discriminator itself is still reported", map[string]any{}, "action"},
		{"an unresolved discriminator cannot be read, so no rule fires", map[string]any{"action": "${step.0.mode}"}, ""},
		{"case and spacing do not hide a mode", map[string]any{"action": " Write "}, "content,name"},
		{"an empty required value counts as missing", map[string]any{"action": "write", "content": "  ", "name": "n"}, "content"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(names(missingRequiredParams(schema, c.params)), ",")
			if got != c.want {
				t.Fatalf("missing = %q, want %q", got, c.want)
			}
		})
	}
}

// The planner has to learn that a parameter became required because of the mode
// it chose, otherwise the correction it makes is a guess.
func TestValidatePlanParams_ConditionalErrorNamesTheMode(t *testing.T) {
	reg := toolapi.NewRegistry()
	reg.Replace(&fakeTool{name: "clipboard", params: json.RawMessage(modeSchema)}, "builtin")

	errs := validatePlanParams([]PlanStep{
		{Tool: "clipboard", Params: map[string]any{"action": "write", "name": "n"}},
	}, reg)

	if len(errs) != 1 {
		t.Fatalf("expected one error, got %v", errs)
	}
	if !strings.Contains(errs[0], `"content"`) || !strings.Contains(errs[0], `action is "write"`) {
		t.Fatalf("the error must name the parameter and the mode that demands it, got %q", errs[0])
	}
}

func TestValidateDirectParams_RejectsMissingConditional(t *testing.T) {
	tool := &fakeTool{name: "clipboard", params: json.RawMessage(modeSchema)}
	if err := validateDirectParams(tool, map[string]any{"action": "read"}); err != nil {
		t.Fatalf("read needs nothing more: %v", err)
	}
	err := validateDirectParams(tool, map[string]any{"action": "write", "name": "n"})
	if err == nil || !strings.Contains(err.Error(), "content") {
		t.Fatalf("write without content must be rejected, got %v", err)
	}
}
