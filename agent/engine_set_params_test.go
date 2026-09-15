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
	"strings"
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

// task_files end to end, through every stage that reads a tool's schema.
//
// It used to be declared on compute with "DEPRECATED on compute — use the
// edit_file tool instead" in its own description: the schema offered a planner a
// parameter and told the same planner not to use it, while the architect set it
// on every coder node. A description is advice, and advice is what a planner
// weighs against the rest of its instructions. This is the refusal that advice
// was asking for.
//
// Four stages read the schema and each has to land differently, which is why
// this is one test and not four assertions in separate files: refusing a plan is
// only correct if the architect's node still passes, and dropping it from the
// signature is only correct if edit_file's own route is untouched.

// The planner is not shown it.
func TestTaskFiles_TheSignatureThePlannerReadsDoesNotOfferIt(t *testing.T) {
	_, reg := computeOnlyAgent(t)
	entry := toolIndexEntry(reg, "compute")
	if entry == "" {
		t.Fatal("compute has no index entry")
	}
	sig := entry[:strings.Index(entry, ")")+1]
	if strings.Contains(sig, "task_files") {
		t.Errorf("compute's signature still offers task_files: %s", sig)
	}
	// And the one it is meant to reach for instead still does.
	edit := toolIndexEntry(reg, "edit_file")
	if !strings.Contains(edit[:strings.Index(edit, ")")+1], "task_files") {
		t.Errorf("edit_file's signature no longer offers task_files: %s", edit)
	}
}

// A plan writing it is refused, and told which stage owns it rather than that it
// does not exist — a planner may well have seen the name on a coder node in the
// trace, and "does not exist" would be a lie it cannot act on.
func TestTaskFiles_APlanWritingItIsRefusedAndToldWhy(t *testing.T) {
	_, reg := computeOnlyAgent(t)
	steps := []PlanStep{{Tool: "compute", Tag: "c", Params: map[string]any{
		"goal": "edit the parser", "mode": "shallow",
		"task_files": []any{"parser.go"}}}}
	errs := validatePlanParams(steps, reg)
	if len(errs) == 0 {
		t.Fatal("a plan writing task_files on compute was accepted")
	}
	if !strings.Contains(errs[0], "task_files") {
		t.Errorf("the correction does not name the parameter it refused: %s", errs[0])
	}
	if strings.Contains(errs[0], "does not exist") {
		t.Errorf("the correction says the name does not exist, which is not true of it: %s", errs[0])
	}
	if !strings.Contains(errs[0], "sets") || !strings.Contains(errs[0], "a plan does not write it") {
		t.Errorf("the correction does not say which stage owns it: %s", errs[0])
	}
}

// The architect's coder node carries it and still dispatches. This is the half
// that fails if task_files is simply deleted rather than moved.
func TestTaskFiles_AnArchitectsCoderNodeStillCarriesIt(t *testing.T) {
	_, reg := computeOnlyAgent(t)
	tool, _ := reg.Get("compute")
	params := map[string]any{
		"goal": "build the parser", "mode": "shallow",
		"task_files": []any{"project/s-1/webapp/parser.go"},
	}
	if err := validateDirectParams(tool, params); err != nil {
		t.Fatalf("a coder node carrying task_files was refused at dispatch: %v", err)
	}
}

// edit_file's own route is untouched: it declares task_files, requires it, and
// hands it to compute through a Go call that never meets either validator.
func TestTaskFiles_EditFilesOwnRouteIsUnchanged(t *testing.T) {
	_, reg := computeOnlyAgent(t)
	tool, ok := reg.Get("edit_file")
	if !ok {
		t.Fatal("edit_file not registered")
	}
	schema, err := parseToolSchema(tool.Parameters())
	if err != nil {
		t.Fatalf("edit_file schema: %v", err)
	}
	if _, declared := schema.Properties["task_files"]; !declared {
		t.Fatal("edit_file no longer declares task_files")
	}
	var required bool
	for _, r := range schema.Required {
		if r == "task_files" {
			required = true
		}
	}
	if !required {
		t.Error("edit_file no longer requires task_files")
	}
	// A plan using edit_file the way it is meant to is clean.
	steps := []PlanStep{{Tool: "edit_file", Tag: "e", Params: map[string]any{
		"goal": "add the CORS middleware", "task_files": []any{"server.go"}}}}
	if errs := validatePlanParams(steps, reg); len(errs) != 0 {
		t.Errorf("a correct edit_file step was rejected: %v", errs)
	}
}
