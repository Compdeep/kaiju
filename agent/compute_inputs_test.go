package agent

import (
	"strings"
	"testing"
)

// The plan that produced a fabricated answer.
//
// A search, a fetch and a compute, none of them wired to each other. All three
// started in the same second; the compute ran for eighty seconds with the other
// two results sitting in the graph where it could not reach them, and wrote the
// figures it needed as constants. Every check the engine ran passed.
func TestComputeInputs_CatchesTheUnwiredCompute(t *testing.T) {
	plan := []PlanStep{
		{Tool: "web_search", Tag: "find_info", Params: map[string]any{"query": "barycenter"}},
		{Type: "compute", Tool: "compute", Tag: "calculate", Params: map[string]any{
			"goal": "calculate the barycenter", "mode": "shallow",
			"context": "Current date: 2026-08-29",
		}},
		{Tool: "web_fetch", Tag: "read_it", Params: map[string]any{"url": "https://example.org/a"}},
	}
	errs := validatePlanComputeInputs(plan)
	if len(errs) != 1 {
		t.Fatalf("the unwired compute was not reported: %v", errs)
	}
	// The message has to name what it could have read, or the next attempt guesses.
	if !strings.Contains(errs[0], "find_info") || !strings.Contains(errs[0], "read_it") {
		t.Errorf("the fault does not name the steps it ignored: %q", errs[0])
	}
	if !strings.Contains(errs[0], "${step.") {
		t.Errorf("the fault does not say how to fix it: %q", errs[0])
	}
}

// A compute that reads a step is doing what it should, however it reads it.
func TestComputeInputs_AcceptsAWiredCompute(t *testing.T) {
	plan := []PlanStep{
		{Tool: "file_read", Tag: "read_csv", Params: map[string]any{"path": "x.csv"}},
		{Type: "compute", Tool: "compute", Tag: "rank", Params: map[string]any{
			"goal": "rank the rows", "context.csv": "${step.read_csv.content}",
		}},
	}
	if errs := validatePlanComputeInputs(plan); len(errs) != 0 {
		t.Errorf("a correctly wired compute was reported: %v", errs)
	}
}

// A step that DECLARES a dependency and wires nothing is the same fault in a
// different shape, and validatePlanWiring owns it — it drops the step so the run
// continues without it. Reporting it here as well would put two remedies on one
// fault, and this one fails the run.
func TestComputeInputs_LeavesTheDeclaredDependencyCaseToItsOwner(t *testing.T) {
	plan := []PlanStep{
		{Tool: "process_list", Tag: "procs"},
		{Type: "compute", Tool: "compute", Tag: "build", DependsOn: []int{0},
			Params: map[string]any{"goal": "use ${node. and stop", "mode": "shallow"}},
	}
	if errs := validatePlanComputeInputs(plan); len(errs) != 0 {
		t.Errorf("a fault validatePlanWiring already handles was reported twice: %v", errs)
	}
}

// A calculation that needs nothing this plan gathers is not a fault. The
// thousandth prime, a unit conversion, a date difference — there is nothing to
// wire, and a check that demanded wiring anyway would fail those runs after
// three corrections.
func TestComputeInputs_LeavesAStandaloneCalculationAlone(t *testing.T) {
	alone := []PlanStep{
		{Type: "compute", Tool: "compute", Tag: "primes", Params: map[string]any{
			"goal": "the thousandth prime", "mode": "shallow"}},
	}
	if errs := validatePlanComputeInputs(alone); len(errs) != 0 {
		t.Errorf("a compute with nothing to read from was reported: %v", errs)
	}
}

// A step that reads the compute is its output, not an input it ignored.
// Counting it would report every compute that writes its result somewhere.
func TestComputeInputs_ADownstreamStepIsNotAMissedInput(t *testing.T) {
	plan := []PlanStep{
		{Type: "compute", Tool: "compute", Tag: "calc", Params: map[string]any{
			"goal": "work it out", "mode": "shallow"}},
		{Tool: "file_write", Tag: "save", Params: map[string]any{
			"path": "out.txt", "content": "${step.calc.output}"}},
	}
	if errs := validatePlanComputeInputs(plan); len(errs) != 0 {
		t.Errorf("a compute whose result is written out was reported as unwired: %v", errs)
	}
}

// task_files is the other way data reaches a compute: it opens them itself
// rather than being handed their content.
func TestComputeInputs_TaskFilesCount(t *testing.T) {
	plan := []PlanStep{
		{Tool: "file_list", Tag: "list", Params: map[string]any{"path": "."}},
		{Type: "compute", Tool: "compute", Tag: "edit", Params: map[string]any{
			"goal": "fix the export", "task_files": []any{"project/app.js"}}},
	}
	if errs := validatePlanComputeInputs(plan); len(errs) != 0 {
		t.Errorf("a compute reading files from disk was reported as having no data: %v", errs)
	}
}

// A compute that reads nothing is a suspicion, not a certainty — so it is told
// to the planner and does not end the run.
//
// The certain faults do end it: a name matching nothing, a parameter a tool does
// not take. Those are wrong however the run turns out. Whether a compute needs
// what the plan gathered depends on the goal, which nothing at plan time knows,
// and each correction is a heavy-model call with the whole plan prompt behind
// it. What survives is caught in the compute's own prompt instead.
func TestComputeInputs_IsAdvisoryAndDoesNotFailTheRun(t *testing.T) {
	src := readSource(t, "executive.go")

	// The fatal set is the four certain checks, and inputErrs is not among them.
	fatal := indexOf(src, "fatal := append(append(append(append([]string{}, nameErrs...), refErrs...), paramErrs...), depErrs...)")
	if fatal < 0 {
		t.Fatal("the certain faults are no longer separated from the advisory one")
	}
	if strings.Contains(src, "fatal := append(append(append(append(append([]string{}, nameErrs") {
		t.Error("inputErrs is back in the set that ends the run")
	}
	// And it still reaches the planner, or telling it would be pointless.
	if !strings.Contains(src, "allErrs := append(append([]string{}, fatal...), inputErrs...)") {
		t.Error("inputErrs no longer reaches the correction feedback")
	}
}
