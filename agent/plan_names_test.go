package agent

import "testing"

// A name is what a reference resolves against, so it has to name one step.
//
// stepIndexFor takes the FIRST match, so two steps sharing a name meant every
// reference to it silently read one of them. Reporting it back did not fix it:
// three corrections cost a reasoning-model call each and still ended a live
// run with no answer. The later step is renamed instead, and the first keeps
// the name so the references already written against it land where they did.
func TestResolvePlanNames_RenamesADuplicate(t *testing.T) {
	steps := []PlanStep{
		{Tool: "web_fetch", Tag: "fetch_page"},
		{Tool: "web_fetch", Tag: "fetch_page"},
		{Tool: "web_fetch", Tag: "fetch_page"},
	}
	if errs := resolvePlanNames(steps); len(errs) != 0 {
		t.Fatalf("a duplicate is renamed, not reported: %v", errs)
	}
	if steps[0].Tag != "fetch_page" {
		t.Errorf("the first occurrence keeps the name, so existing references still resolve: %q", steps[0].Tag)
	}
	if steps[1].Tag != "fetch_page_2" || steps[2].Tag != "fetch_page_3" {
		t.Errorf("each later step needs its own name, got %q and %q", steps[1].Tag, steps[2].Tag)
	}
}

// The suffix has to skip a name the plan already spends, or the rename walks
// straight into a second clash.
func TestResolvePlanNames_SkipsASuffixThePlanAlreadyUses(t *testing.T) {
	steps := []PlanStep{
		{Tool: "web_fetch", Tag: "fetch_page"},
		{Tool: "web_fetch", Tag: "fetch_page_2"},
		{Tool: "web_fetch", Tag: "fetch_page"},
	}
	if errs := resolvePlanNames(steps); len(errs) != 0 {
		t.Fatalf("a duplicate is renamed, not reported: %v", errs)
	}
	if steps[2].Tag != "fetch_page_3" {
		t.Errorf("want fetch_page_3, past the name step 1 holds, got %q", steps[2].Tag)
	}
}

// A renamed step still has to be a name a reference can spell, or the rename
// has swapped a duplicate for something unreachable.
func TestResolvePlanNames_RenameStaysSpellable(t *testing.T) {
	steps := []PlanStep{{Tool: "bash", Tag: "check"}, {Tool: "bash", Tag: "check"}}
	resolvePlanNames(steps)
	if !stepNameRe.MatchString(steps[1].Tag) {
		t.Errorf("a reference cannot address %q", steps[1].Tag)
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
		errs := resolvePlanNames([]PlanStep{{Tool: "file_read", Tag: name}})
		if len(errs) != 1 {
			t.Errorf("%q was accepted as a name: %v", name, errs)
		}
	}
}

// Any script, because the rule is about delimiters and not about English.
func TestValidatePlanNames_AcceptsAnyScript(t *testing.T) {
	for _, name := range []string{"read_csv", "read-csv", "step2", "读取文件", "чтение"} {
		if errs := resolvePlanNames([]PlanStep{{Tool: "file_read", Tag: name}}); len(errs) != 0 {
			t.Errorf("%q was rejected: %v", name, errs)
		}
	}
	if m := stepTemplateRe.FindStringSubmatch("${step.读取文件.content}"); m == nil || m[1] != "读取文件" {
		t.Errorf("a reference to a non-Latin name did not parse: %v", m)
	}
}

// An unnamed step is addressed by position, so there is nothing to clash.
func TestValidatePlanNames_UnnamedStepsAreFine(t *testing.T) {
	if errs := resolvePlanNames([]PlanStep{
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
	if errs := resolvePlanNames([]PlanStep{{Tool: "bash", Tag: n.Tag}}); len(errs) != 0 {
		t.Errorf("the name stopped being referenceable after a retry: %v", errs)
	}
	if info := g.SnapshotNode(id); info == nil || info.Retry != "blind" {
		t.Error("the trace cannot tell that this step was retried")
	}
}
