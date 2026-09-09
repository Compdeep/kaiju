package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// params cannot be described in one object: it carries whatever the NAMED
// tool's signature needs, and the tool is chosen by a sibling field. So it said
// "anything", and under a strict wire that is a contradiction — the shape binds
// and the shape is unbounded. Each model resolved it differently and all three
// resolved it wrong: invented tool names with invented parameters, or the real
// shape with params left empty.

// A tool whose parameters are actually declared. The shared harness registers
// stubs whose schema is `{"type":"object"}` and nothing else — which the builder
// correctly refuses, since it constrains no key.
type describedTool struct {
	name   string
	params string
}

func (d *describedTool) Name() string                { return d.name }
func (d *describedTool) Description() string         { return "for the schema tests" }
func (d *describedTool) Impact(map[string]any) int   { return toolapi.ImpactObserve }
func (d *describedTool) Parameters() json.RawMessage { return json.RawMessage(d.params) }
func (d *describedTool) Execute(context.Context, map[string]any) (string, error) {
	return toolapi.ToolOK("ok", "", nil).JSON(), nil
}

func schemaAgent(t *testing.T) *Agent {
	t.Helper()
	reg := toolapi.NewRegistry()
	for _, tl := range []*describedTool{
		{"bash", `{"type":"object","properties":{"command":{"type":"string"},"timeout_sec":{"type":"integer"}},"required":["command"]}`},
		{"file_read", `{"type":"object","properties":{"path":{"type":"string"},"max_lines":{"type":"integer"}},"required":["path"]}`},
		{"web_fetch", `{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`},
	} {
		if err := reg.Register(tl); err != nil {
			t.Fatalf("register %s: %v", tl.name, err)
		}
	}
	return &Agent{registry: reg}
}

// A branch per tool, each pinning the name and carrying that tool's own params.
func TestPlanStepBranches_DescribesEachToolsOwnParams(t *testing.T) {
	a := schemaAgent(t)
	names := a.registry.List()
	raw, ok := planStepBranches(names, a.registry)
	if !ok {
		t.Fatal("no branches were built from a populated registry")
	}
	var doc struct {
		AnyOf []struct {
			Properties struct {
				Tool   struct{ Const string } `json:"tool"`
				Params json.RawMessage        `json:"params"`
			} `json:"properties"`
		} `json:"anyOf"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the schema is not valid JSON: %v", err)
	}
	if len(doc.AnyOf) != len(names) {
		t.Errorf("%d branches for %d tools", len(doc.AnyOf), len(names))
	}
	seen := map[string]bool{}
	for _, b := range doc.AnyOf {
		if b.Properties.Tool.Const == "" {
			t.Error("a branch does not pin its tool name, so it matches any step")
		}
		// An empty object is valid JSON and means "any value", so a branch
		// carrying one is as permissive as the open shape it replaced — the
		// same fault written per tool, and harder to see.
		if !describesSomething(b.Properties.Params) {
			t.Errorf("%s carries a params schema that constrains nothing: %s",
				b.Properties.Tool.Const, string(b.Properties.Params))
		}
		seen[b.Properties.Tool.Const] = true
	}
	for _, n := range names {
		if !seen[n] {
			t.Errorf("%s has no branch, so a step naming it cannot validate", n)
		}
	}
}

// Only the tools the planner was SHOWN. A branch for one it cannot see would
// let it name a tool it was never offered.
func TestPlanStepBranches_OnlyTheToolsShown(t *testing.T) {
	a := schemaAgent(t)
	all := a.registry.List()
	if len(all) < 2 {
		t.Skip("need at least two tools")
	}
	raw, ok := planStepBranches(all[:1], a.registry)
	if !ok {
		t.Fatal("no branches built")
	}
	if strings.Contains(string(raw), `"`+all[1]+`"`) {
		t.Errorf("%s was described though it was not shown", all[1])
	}
}

// Nothing to describe leaves the open shape, because a planner with no plan
// schema at all is worse than one with an unconstrained params.
func TestPlanStepBranches_EmptyFallsBack(t *testing.T) {
	a := schemaAgent(t)
	if _, ok := planStepBranches(nil, a.registry); ok {
		t.Error("an empty tool list produced branches")
	}
	if _, ok := planStepBranches([]string{"no_such_tool"}, a.registry); ok {
		t.Error("an unknown tool produced a branch")
	}
	if _, ok := planStepBranches([]string{"bash"}, nil); ok {
		t.Error("a nil registry produced branches")
	}
}

// The whole schema still parses, and still carries the intent enum it always
// did — the substitution must not have broken the document around it.
func TestExecutivePlanSchema_IsValidWithAndWithoutTools(t *testing.T) {
	a := schemaAgent(t)
	for _, label := range []string{"described", "open"} {
		var def = a.executivePlanSchema()
		if label == "described" {
			def = a.executivePlanSchema(a.registry.List())
		}
		var doc map[string]any
		if err := json.Unmarshal(def.Function.Parameters, &doc); err != nil {
			t.Fatalf("%s: schema is not valid JSON: %v", label, err)
		}
		props, _ := doc["properties"].(map[string]any)
		if props["steps"] == nil {
			t.Errorf("%s: steps is missing", label)
		}
		if props["intent"] == nil {
			t.Errorf("%s: the intent enum was lost", label)
		}
		req, _ := doc["required"].([]any)
		if len(req) == 0 {
			t.Errorf("%s: required was lost", label)
		}
	}
}

// The described schema names real tools; the open one names none, which is how
// a model came to invent them.
func TestExecutivePlanSchema_DescribedNamesTheRealTools(t *testing.T) {
	a := schemaAgent(t)
	described := string(a.executivePlanSchema(a.registry.List()).Function.Parameters)
	open := string(a.executivePlanSchema().Function.Parameters)

	var found string
	for _, n := range a.registry.List() {
		if strings.Contains(described, `"`+n+`"`) {
			found = n
			break
		}
	}
	if found == "" {
		t.Fatal("the described schema names no tool at all")
	}
	if strings.Contains(open, `"`+found+`"`) {
		t.Errorf("the open schema names %s, so the two are not different", found)
	}
}

// An empty object parses and validates and says nothing. A branch built on one
// is the open shape wearing a tool's name.
func TestDescribesSomething_RejectsAVacuousSchema(t *testing.T) {
	for _, raw := range []string{``, `{}`, `   {}  `, `null`, `"a string"`, `not json`} {
		if describesSomething(json.RawMessage(raw)) {
			t.Errorf("%q was accepted as describing something", raw)
		}
	}
}

// A tool that genuinely takes nothing says so, and that is a real constraint.
func TestDescribesSomething_KeepsARealSchema(t *testing.T) {
	for _, raw := range []string{
		`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`,
		`{"type":"object","properties":{},"additionalProperties":false}`,
		`{"$ref":"#/defs/params"}`,
	} {
		if !describesSomething(json.RawMessage(raw)) {
			t.Errorf("%s was rejected", raw)
		}
	}
}

// A registry with no intents must still produce a schema a provider accepts.
//
// An empty list marshals to null, and "enum": null is not a schema — Anthropic
// rejects the request outright, which is a planner that cannot run rather than
// one missing an enum.
func TestExecutivePlanSchema_NoIntentsIsStillValid(t *testing.T) {
	a := schemaAgent(t) // no intentRegistry
	raw := a.executivePlanSchema(a.registry.List()).Function.Parameters
	if strings.Contains(string(raw), `"enum": null`) || strings.Contains(string(raw), `"enum":null`) {
		t.Error(`the schema carries "enum": null, which no provider will accept`)
	}
	var doc struct {
		Properties struct {
			Intent struct {
				Enum []string `json:"enum"`
			} `json:"intent"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if doc.Properties.Intent.Enum == nil {
		t.Error("intent has no enum array at all")
	}
}
