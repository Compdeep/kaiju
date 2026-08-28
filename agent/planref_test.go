package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// A reference is a shape, so it is told apart by its shape and not by parsing a
// string. Params arrive decoded from JSON, so it is a map with a step in it.
func TestRefFrom_ReadsADeclaredReference(t *testing.T) {
	var params map[string]any
	if err := json.Unmarshal([]byte(
		`{"context.csv":{"step":"read_csv","field":"content"},
		  "goal":"rank the rows",
		  "whole":{"step":"read_csv"},
		  "nested":{"inner":{"step":"read_csv","field":"path"}}}`), &params); err != nil {
		t.Fatal(err)
	}

	got := refsIn(params)
	if len(got) != 3 {
		t.Fatalf("found %d references, want 3: %v", len(got), got)
	}
	var whole bool
	for _, r := range got {
		if r.Step != "read_csv" {
			t.Errorf("a reference named %q", r.Step)
		}
		if r.Field == "" {
			whole = true
		}
	}
	if !whole {
		t.Error("a reference with no field means the whole result and must survive")
	}
}

// A parameter that happens to contain "step" is a parameter, not a reference.
// Guessing wrongly here rewrites a tool's own input into a placeholder.
func TestRefFrom_LeavesOrdinaryParametersAlone(t *testing.T) {
	for _, raw := range []string{
		`{"step":"read_csv","field":"content","extra":1}`, // carries more than a reference does
		`{"field":"content"}`,                             // no step
		`{"step":""}`,                                     // no name
		`{"step":3}`,                                      // a number, not a name
		`"just a string"`,
		`["a","b"]`,
	} {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatal(err)
		}
		if r, ok := refFrom(v); ok {
			t.Errorf("%s was read as a reference to %q", raw, r.Step)
		}
	}
}

// The declared shape becomes the node template the dispatcher already
// resolves, so nothing downstream of the graft changes.
func TestRef_RendersTheTemplateTheDispatcherResolves(t *testing.T) {
	if got := (Ref{Step: "read_csv", Field: "content"}).template("n3"); got != "${node.n3.content}" {
		t.Errorf("got %q", got)
	}
	if got := (Ref{Step: "read_csv"}).template("n3"); got != "${node.n3}" {
		t.Errorf("a reference with no field must ask for the whole result, got %q", got)
	}
	if got := (Ref{Step: "s", Field: "results.0.url"}).template("n1"); got != "${node.n1.results.0.url}" {
		t.Errorf("a dot-path must survive, got %q", got)
	}
}

// A step that references another depends on it by saying so. The plan does not
// state the dependency, and nothing has to keep the two in agreement.
func TestResolveDeclaredRefs_DerivesTheDependency(t *testing.T) {
	steps := []PlanStep{
		{Tool: "file_read", Tag: "read_csv"},
		{Tool: "compute", Tag: "rank", Params: map[string]any{
			"context.csv": map[string]any{"step": "read_csv", "field": "content"},
		}},
	}
	n := &Node{ID: "n2", Params: steps[1].Params}

	deps := resolveDeclaredRefs(n, []string{"n1", "n2"}, steps, nil)

	if len(deps) != 1 || deps[0] != "n1" {
		t.Fatalf("dependency not derived from the reference: %v", deps)
	}
	if got := n.Params["context.csv"]; got != "${node.n1.content}" {
		t.Errorf("the reference was not rewritten: %v", got)
	}
}

// A reference naming no step is left as it was. It has already been reported;
// rewriting it into a template pointing nowhere turns a named fault into a
// silent one.
func TestResolveDeclaredRefs_LeavesAnUnknownNameAlone(t *testing.T) {
	steps := []PlanStep{
		{Tool: "compute", Tag: "rank", Params: map[string]any{
			"in": map[string]any{"step": "no_such_step", "field": "content"},
		}},
	}
	n := &Node{ID: "n1", Params: steps[0].Params}

	if deps := resolveDeclaredRefs(n, []string{"n1"}, steps, nil); len(deps) != 0 {
		t.Errorf("an unknown name produced a dependency: %v", deps)
	}
	if _, rewritten := n.Params["in"].(string); rewritten {
		t.Error("an unknown name was rewritten into a template pointing nowhere")
	}
}

// The three faults, reported before the plan runs.
func TestValidatePlanReferences_CatchesDeclaredFaults(t *testing.T) {
	reg := envRegistry(t)

	unknown := []PlanStep{
		{Tool: "file_read", Tag: "read_csv"},
		{Tool: "compute", Tag: "rank", Params: map[string]any{
			"in": map[string]any{"step": "nope", "field": "content"}}},
	}
	if errs := validatePlanReferences(unknown, reg); len(errs) == 0 {
		t.Error("a reference to a step that does not exist was accepted")
	} else if !strings.Contains(errs[0], "read_csv") {
		t.Errorf("the fault must say what the plan DOES have: %q", errs[0])
	}

	self := []PlanStep{
		{Tool: "compute", Tag: "rank", Params: map[string]any{
			"in": map[string]any{"step": "rank", "field": "content"}}},
	}
	if errs := validatePlanReferences(self, reg); len(errs) == 0 {
		t.Error("a step referencing itself was accepted")
	}

	wrongField := []PlanStep{
		{Tool: "file_read", Tag: "read_csv"},
		{Tool: "compute", Tag: "rank", Params: map[string]any{
			"in": map[string]any{"step": "read_csv", "field": "results"}}},
	}
	if errs := validatePlanReferences(wrongField, reg); len(errs) == 0 {
		t.Error("a field the producing tool does not return was accepted")
	}
}

// The envelope's own fields are returnable, which is the fault that rejected
// every correct file_read → compute plan for as long as it stood.
func TestValidatePlanReferences_DeclaredContentIsAccepted(t *testing.T) {
	steps := []PlanStep{
		{Tool: "file_read", Tag: "read_csv"},
		{Tool: "compute", Tag: "rank", Params: map[string]any{
			"in": map[string]any{"step": "read_csv", "field": "content"}}},
	}
	if errs := validatePlanReferences(steps, envRegistry(t)); len(errs) != 0 {
		t.Errorf("a correct plan was rejected: %v", errs)
	}
}
