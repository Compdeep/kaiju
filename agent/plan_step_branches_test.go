package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The tests that shipped with the first attempt at this all asked whether the
// union was BUILT — a branch per tool, only the tools shown, an empty list
// falling back, the document parsing. Eight of them, all green, while the thing
// they described was never enforced: the branches sat behind anyOf and the
// closer of the day walked only properties and items.properties, so it reached
// steps.items, found no properties, and returned.
//
// These ask the question that was missing. The schema goes through the real
// rewrite and is judged by the real checker, which is what a provider does.

type branchTool struct {
	name   string
	params string
}

func (d *branchTool) Name() string                { return d.name }
func (d *branchTool) Description() string         { return "for the branch tests" }
func (d *branchTool) Impact(map[string]any) int   { return toolapi.ImpactObserve }
func (d *branchTool) Parameters() json.RawMessage { return json.RawMessage(d.params) }
func (d *branchTool) Execute(context.Context, map[string]any) (string, error) {
	return toolapi.ToolOK("ok", "", nil).JSON(), nil
}

func branchAgent(t *testing.T, tools ...*branchTool) *Agent {
	t.Helper()
	if len(tools) == 0 {
		tools = []*branchTool{
			{"bash", `{"type":"object","properties":{"command":{"type":"string"},"timeout_sec":{"type":"integer"}},"required":["command"],"additionalProperties":false}`},
			{"file_read", `{"type":"object","properties":{"path":{"type":"string"},"max_lines":{"type":"integer"}},"required":["path"],"additionalProperties":false}`},
		}
	}
	reg := toolapi.NewRegistry()
	for _, tl := range tools {
		if err := reg.Register(tl); err != nil {
			t.Fatalf("register %s: %v", tl.name, err)
		}
	}
	a := &Agent{registry: reg, intentRegistry: NewIntentRegistry()}
	if err := a.intentRegistry.Load(staticIntents{}); err != nil {
		t.Fatalf("load intents: %v", err)
	}
	return a
}

// The document the planner sends is one a strict provider accepts.
//
// This is the test whose absence let the first attempt ship. It fails the
// moment the closer cannot reach a branch, whatever the reason.
func TestPlanSchema_WithBranchesIsAcceptableUnderStrict(t *testing.T) {
	a := branchAgent(t)
	sent := llm.SchemaAsSent(a.executivePlanSchema(a.registry.List()))
	if sent == nil {
		t.Fatal("the plan schema would not be sent as a schema request at all")
	}
	// This is also exactly the guard's condition in asSchemaRequest: no problems
	// means the request is converted and labelled strict, and any problem means
	// it stays on tool calling.
	if p := llm.StrictProblems(sent); len(p) > 0 {
		t.Errorf("a provider enforcing strict would refuse %d thing(s):", len(p))
		for _, x := range p {
			t.Errorf("  %s", x)
		}
	}
}

// Each tool's own parameters survive to the wire, pinned to that tool's name.
// This is the whole point: naming the tool is what binds the shape.
func TestPlanSchema_EachBranchPinsItsToolAndCarriesItsParams(t *testing.T) {
	a := branchAgent(t)
	sent := llm.SchemaAsSent(a.executivePlanSchema(a.registry.List()))
	if sent == nil {
		t.Fatal("the plan schema would not be sent")
	}
	var doc struct {
		Properties struct {
			Steps struct {
				Items struct {
					AnyOf []struct {
						Properties struct {
							Tool struct {
								Const string `json:"const"`
							} `json:"tool"`
							Params struct {
								Properties map[string]any `json:"properties"`
							} `json:"params"`
						} `json:"properties"`
					} `json:"anyOf"`
				} `json:"items"`
			} `json:"steps"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(sent, &doc); err != nil {
		t.Fatalf("the sent schema is not the shape expected: %v", err)
	}
	branches := doc.Properties.Steps.Items.AnyOf
	if len(branches) != 2 {
		t.Fatalf("%d branches, want one per registered tool", len(branches))
	}
	want := map[string]string{"bash": "command", "file_read": "path"}
	for _, b := range branches {
		tool := b.Properties.Tool.Const
		param, known := want[tool]
		if !known {
			t.Errorf("a branch pins an unknown tool %q", tool)
			continue
		}
		if _, has := b.Properties.Params.Properties[param]; !has {
			t.Errorf("%s's branch does not carry its %q parameter: %v",
				tool, param, b.Properties.Params.Properties)
		}
		delete(want, tool)
	}
	if len(want) > 0 {
		t.Errorf("no branch for %v", want)
	}
}

// Only the tools the planner was SHOWN. A branch for one it cannot see would
// let it name a tool it was never offered, which is the narrowing undone.
func TestPlanStepBranches_OnlyTheToolsShown(t *testing.T) {
	a := branchAgent(t)
	raw, ok := planStepBranches([]string{"bash"}, a.registry)
	if !ok {
		t.Fatal("no branches were built from one shown tool")
	}
	if strings.Contains(string(raw), "file_read") {
		t.Error("a tool the planner was not shown has a branch")
	}
}

// One tool strict cannot express costs the whole document, because strict is
// all-or-nothing per schema.
//
// Leaving that tool out instead would be worse: with no branch pinning its
// name, the model could not name it at all, and a plan that needs it could not
// be written. So the plan falls back to the open shape, the guard declines to
// claim strict, and the tool keeps working.
func TestPlanStepBranches_OneInexpressibleToolFallsTheWholePlanBack(t *testing.T) {
	// A map with undeclared keys — what web_fetch's `headers` is, and what
	// strict has no way to describe.
	a := branchAgent(t,
		&branchTool{"bash", `{"type":"object","properties":{"command":{"type":"string"}},"required":["command"],"additionalProperties":false}`},
		&branchTool{"web_fetch", `{"type":"object","properties":{"url":{"type":"string"},"headers":{"type":"object","additionalProperties":{"type":"string"}}},"required":["url"],"additionalProperties":false}`},
	)
	if _, ok := planStepBranches(a.registry.List(), a.registry); ok {
		t.Error("a union was built around a tool strict cannot carry")
	}
	sent := llm.SchemaAsSent(a.executivePlanSchema(a.registry.List()))
	if sent == nil {
		return
	}
	if len(llm.StrictProblems(sent)) == 0 {
		t.Error("the fallback document claims to be enforceable")
	}
}

// A tool whose parameters say nothing is judged by the real closer, not by
// whether the document mentions the word "type".
//
// {"type":"object"} passes a keyword test and constrains nothing — a branch
// carrying it is exactly as permissive as the open shape it replaced, written
// per tool and harder to see because the schema now looks specific.
func TestCloseableParams_JudgesByTheRealCloser(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  string
		want bool
	}{
		{"a real schema", `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`, true},
		{"declares nothing", `{"type":"object"}`, false},
		{"empty object", `{}`, false},
		{"a map with undeclared keys", `{"type":"object","properties":{"h":{"type":"object","additionalProperties":{"type":"string"}}},"required":["h"]}`, false},
		{"not JSON", `{nope`, false},
		// A tool that genuinely takes nothing says so with properties {} and
		// additionalProperties false, which forbids every key. That is a real
		// constraint and is kept — unlike {}, which forbids nothing.
		{"a tool that takes nothing", `{"type":"object","properties":{},"additionalProperties":false}`, true},
	} {
		if got := closeableParams(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("%s: closeableParams = %v, want %v", c.name, got, c.want)
		}
	}
}

// No tools, no registry: the open shape, not a crash and not a union of nothing.
func TestPlanStepBranches_NothingToDescribe(t *testing.T) {
	a := branchAgent(t)
	if _, ok := planStepBranches(nil, a.registry); ok {
		t.Error("an empty tool list produced a union")
	}
	if _, ok := planStepBranches([]string{"bash"}, nil); ok {
		t.Error("a nil registry produced a union")
	}
	if _, ok := planStepBranches([]string{"never_registered"}, a.registry); ok {
		t.Error("a tool the registry does not hold produced a union")
	}
}
