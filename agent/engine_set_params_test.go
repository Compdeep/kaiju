package agent

// Deep mode does not run the planner's compute step. The architect breaks the
// work into items and the scheduler grafts a shallow compute node per item,
// carrying what that coder needs to agree with the others — the blueprint, its
// brief, the workspace tree, the shared signatures, anything to run afterwards.
// Those are built in Go from the architect's reply; no model writes them.
//
// Closing compute's schema refused every one of them at dispatch, which is how
// this was found. The two tests here are the two halves of the fix, and each
// fails on its own if the other is done alone: declaring those parameters in the
// schema fixes dispatch and takes the plan document off strict, because
// interfaces is a map whose keys only the architect knows.

import (
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

func computeOnlyAgent(t *testing.T) (*Agent, *toolapi.Registry) {
	t.Helper()
	reg := toolapi.NewRegistry()
	a := &Agent{intentRegistry: NewIntentRegistry(), registry: reg}
	if err := a.intentRegistry.Load(staticIntents{}); err != nil {
		t.Fatalf("intents: %v", err)
	}
	for _, tl := range []toolapi.Tool{NewComputeTool(a), NewEditFileTool(a)} {
		if err := reg.Register(tl); err != nil {
			t.Fatalf("register %s: %v", tl.Name(), err)
		}
	}
	return a, reg
}

// A coder node the architect built reaches its tool.
func TestEngineSetParams_AnArchitectsCoderNodeIsNotRefusedAtDispatch(t *testing.T) {
	_, reg := computeOnlyAgent(t)
	tool, ok := reg.Get("compute")
	if !ok {
		t.Fatal("compute not registered")
	}
	// Exactly the shape computePlan builds for each of the architect's work
	// items, including the two that are not plain scalars.
	params := map[string]any{
		"goal": "build the parser", "mode": "shallow", "query": "the user's question",
		"context":       []any{"spec=${node.n1.content}"},
		"blueprint_ref": "/workspace/plan.md",
		"task_files":    []any{"parser.go"},
		"brief":         "parse the header block",
		"structure":     "cmd/\n  main.go\n",
		"interfaces":    map[string]any{"Parser": "func Parse(b []byte) (Doc, error)"},
		"execute":       "go test ./...",
		"service":       map[string]any{"command": "./server", "name": "api"},
	}
	if err := validateDirectParams(tool, params); err != nil {
		t.Fatalf("a coder node the architect built was refused: %v", err)
	}
}

// And naming one of them in a PLAN is still refused, because a planner choosing
// a blueprint path or a set of signatures is guessing at something only the
// architect can know.
func TestEngineSetParams_APlanStillMayNotWriteThem(t *testing.T) {
	_, reg := computeOnlyAgent(t)
	steps := []PlanStep{{Tool: "compute", Tag: "c", Params: map[string]any{
		"goal": "build it", "mode": "deep", "blueprint_ref": "/guessed/plan.md"}}}
	errs := validatePlanParams(steps, reg)
	if len(errs) == 0 {
		t.Fatal("a plan naming an engine-set parameter was accepted")
	}
}

// The plan document stays strict-carryable. This is the half that fails if the
// engine-set parameters are declared in the schema instead: interfaces is a map
// with undeclared keys, and one such field takes the whole document off strict
// rather than just compute's branch.
func TestEngineSetParams_TheyAreNotInTheSchemaTheProviderReceives(t *testing.T) {
	a, reg := computeOnlyAgent(t)
	sent := llm.SchemaAsSent(a.executivePlanSchema(reg.List()))
	if sent == nil {
		t.Fatal("the plan schema would not be sent as a schema request at all")
	}
	for _, p := range llm.StrictProblems(sent) {
		t.Errorf("the plan document cannot be enforced: %s: %s", p.Path, p.Why)
	}
}
