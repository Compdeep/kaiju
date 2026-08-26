package agent

import (
	"strings"
	"testing"
)

// A name is what a reference resolves against, so it has to name one step.
//
// stepIndexFor takes the FIRST match, so two steps sharing a name meant every
// reference to it silently read one of them. Nothing rejected the plan, and
// nothing said which step a reference had reached.
func TestValidatePlanNames_RejectsADuplicate(t *testing.T) {
	errs := validatePlanNames([]PlanStep{
		{Tool: "web_fetch", Tag: "fetch_page"},
		{Tool: "web_fetch", Tag: "fetch_page"},
	})
	if len(errs) != 1 {
		t.Fatalf("want one error for the duplicate, got %d: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0], "fetch_page") || !strings.Contains(errs[0], "step 0") {
		t.Errorf("the error must name the clash and the step it clashes with: %q", errs[0])
	}
}

// A name a reference cannot spell is a name nothing can reach. The reference is
// read as ${step.<name>.<field>}, so a space, a dot or a bracket ends the name
// somewhere the writer did not mean.
func TestValidatePlanNames_RejectsWhatAReferenceCannotSpell(t *testing.T) {
	for _, name := range []string{
		"read the csv",           // a space
		"read_csv [blind_retry]", // what a retry used to append
		"read.csv",               // the separator
		"read}csv",               // the terminator
	} {
		errs := validatePlanNames([]PlanStep{{Tool: "file_read", Tag: name}})
		if len(errs) != 1 {
			t.Errorf("%q was accepted as a name: %v", name, errs)
		}
	}
}

// Any script, because the rule is about delimiters and not about English.
func TestValidatePlanNames_AcceptsAnyScript(t *testing.T) {
	for _, name := range []string{"read_csv", "read-csv", "step2", "读取文件", "чтение"} {
		if errs := validatePlanNames([]PlanStep{{Tool: "file_read", Tag: name}}); len(errs) != 0 {
			t.Errorf("%q was rejected: %v", name, errs)
		}
	}
	if m := stepTemplateRe.FindStringSubmatch("${step.读取文件.content}"); m == nil || m[1] != "读取文件" {
		t.Errorf("a reference to a non-Latin name did not parse: %v", m)
	}
}

// An unnamed step is addressed by position, so there is nothing to clash.
func TestValidatePlanNames_UnnamedStepsAreFine(t *testing.T) {
	if errs := validatePlanNames([]PlanStep{
		{Tool: "web_fetch"}, {Tool: "web_fetch"},
	}); len(errs) != 0 {
		t.Errorf("unnamed steps were rejected: %v", errs)
	}
}

// A retry must not rename the step it retries.
//
// The tier used to be spelled into the name — " [blind_retry]" appended — and
// the guard against retrying twice looked for a bracket. That renames a step
// mid-run, and renames it to something no reference can address: the space and
// the brackets are outside what a name may hold.
func TestNode_ARetryDoesNotRenameTheStep(t *testing.T) {
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, ToolName: "bash", Tag: "clone_repo"})

	g.SetRetry(id, "blind")

	n := g.Get(id)
	if n.Tag != "clone_repo" {
		t.Errorf("the retry renamed the step to %q", n.Tag)
	}
	if n.Retry != "blind" {
		t.Errorf("the tier was not recorded, so nothing stops a second retry: %q", n.Retry)
	}
	if errs := validatePlanNames([]PlanStep{{Tool: "bash", Tag: n.Tag}}); len(errs) != 0 {
		t.Errorf("the name stopped being referenceable after a retry: %v", errs)
	}
	if info := g.SnapshotNode(id); info == nil || info.Retry != "blind" {
		t.Error("the trace cannot tell that this step was retried")
	}
}
