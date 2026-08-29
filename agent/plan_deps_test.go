package agent

import (
	"strings"
	"testing"
)

// A re-plan names an earlier step by its tag, because positions restart with a
// new plan and tags do not. FlexInts can only hold positions, so a name was
// dropped as it decoded and the step lost the dependency without anyone being told.
func TestPlanDependsOn_ResolvesTagsAndReportsTheRest(t *testing.T) {
	raw := `{"steps":[
		{"tool":"reader","tag":"fetch_page","depends_on":[]},
		{"tool":"bash","tag":"middle","depends_on":[0]},
		{"tool":"compute","tag":"work","depends_on":["fetch_page","1"]},
		{"tool":"compute","tag":"stray","depends_on":["never_planned"]},
		{"tool":"compute","tag":"itself","depends_on":["itself"]}
	]}`

	var payload executiveCallPayload
	if err := parseExecutivePayload(raw, &payload); err != nil {
		t.Fatalf("plan should parse: %v", err)
	}
	steps := payload.Steps

	// A tag resolves to the step it names, and a position written as a string
	// keeps working alongside it.
	if got := steps[2].DependsOn; len(got) != 2 || got[0] != 1 || got[1] != 0 {
		t.Fatalf("work should depend on both the string position and the tag, got %v", got)
	}
	if len(steps[2].UnresolvedDeps) != 0 {
		t.Fatalf("both names resolved, so nothing should be left over: %v", steps[2].UnresolvedDeps)
	}

	// A plain position is untouched.
	if got := steps[1].DependsOn; len(got) != 1 || got[0] != 0 {
		t.Fatalf("a numeric dependency must survive unchanged, got %v", got)
	}

	// A name matching no tag is kept and reported, not dropped.
	if got := steps[3].UnresolvedDeps; len(got) != 1 || got[0] != "never_planned" {
		t.Fatalf("an unknown name must be kept for reporting, got %v", got)
	}
	// A step naming its own tag would depend on itself, which never resolves.
	if got := steps[4].UnresolvedDeps; len(got) != 1 || got[0] != "itself" {
		t.Fatalf("a self-reference must be reported rather than wired, got %v", got)
	}
	if len(steps[4].DependsOn) != 0 {
		t.Fatalf("a step must never depend on itself, got %v", steps[4].DependsOn)
	}

	// And the plan comes back for correction rather than running short a dependency.
	errs := validatePlanDeps(steps)
	if len(errs) != 2 {
		t.Fatalf("both unresolved dependencies must be reported, got %v", errs)
	}
	joined := strings.Join(errs, "\n")
	for _, want := range []string{"never_planned", "itself", `"fetch_page"`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the report must name %s and the tags that do exist, got:\n%s", want, joined)
		}
	}
}

// A clean plan produces nothing, so the correction loop is not entered for it.
func TestPlanDependsOn_CleanPlanReportsNothing(t *testing.T) {
	var payload executiveCallPayload
	if err := parseExecutivePayload(`{"steps":[
		{"tool":"reader","tag":"a","depends_on":[]},
		{"tool":"compute","tag":"b","depends_on":["a"]}
	]}`, &payload); err != nil {
		t.Fatalf("plan should parse: %v", err)
	}
	if got := payload.Steps[1].DependsOn; len(got) != 1 || got[0] != 0 {
		t.Fatalf("the tag should have resolved, got %v", got)
	}
	if errs := validatePlanDeps(payload.Steps); len(errs) != 0 {
		t.Fatalf("a clean plan must report nothing, got %v", errs)
	}
}
