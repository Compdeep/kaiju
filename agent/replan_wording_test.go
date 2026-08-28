package agent

import (
	"strings"
	"testing"
)

// The re-plan prompt used to point the planner at a section that no code
// renders: "(shown above as DATA) do you paste it in literally". Nothing is
// labelled DATA — a prior arc's values arrive as tool results. A planner
// following the instruction has nowhere to look, and the only handle in front
// of it is a tag in the worklog, which is what it then referenced.
func TestReplanFrameDoesNotNameASectionNothingRenders(t *testing.T) {
	if strings.Contains(replanFrameTemplate, "as DATA") {
		t.Error("the re-plan frame still sends the planner to a DATA section; " +
			"the prompt has ## Context, ## Re-plan and ## System State, and no code writes a DATA block")
	}
	// What replaced it has to say where the values actually are.
	if !strings.Contains(replanFrameTemplate, "tool result") {
		t.Error("the re-plan frame no longer says where a finished step's value can be read")
	}
	// And that neither addressing form reaches back — the old text warned only
	// about a "prior-frame index", so a tag looked permitted.
	if !strings.Contains(replanFrameTemplate, "neither its position nor its tag") {
		t.Error("the re-plan frame warns about only one addressing form again; " +
			"a tag is as unreachable as an index, and the tag is the one that was used")
	}
}

// The correction for a reference that names no step in this plan used to end
// "use a step's position (counted from 0) or its exact tag" — and an exact tag
// is exactly what the planner had used. Four byte-identical plans came back on
// one run, because every correction restated the move that had just failed.
func TestTheNoSuchStepCorrectionDoesNotRestateTheFailedMove(t *testing.T) {
	steps := []PlanStep{
		{Tool: "web_search", Tag: "search_server_count", Params: map[string]any{"query": "x"}},
		{Tool: "compute", Tag: "calc", Params: map[string]any{
			"context.tam": "${step.fetch_tam.content}", // a tag from an EARLIER arc
		}},
	}
	errs := validatePlanReferences(steps, nil)
	if len(errs) == 0 {
		t.Fatal("a reference to a step outside this plan was accepted")
	}
	msg := strings.Join(errs, " ")
	if !strings.Contains(msg, "fetch_tam") {
		t.Errorf("the correction does not name the reference that failed: %q", msg)
	}
	// The fix it offers must not be the thing that just failed.
	if strings.Contains(msg, "or its exact tag") && !strings.Contains(msg, "earlier in the run") {
		t.Errorf("the correction still offers the failed move as the remedy: %q", msg)
	}
	if !strings.Contains(msg, "copy the value") {
		t.Errorf("the correction does not say what to do instead: %q", msg)
	}
}
