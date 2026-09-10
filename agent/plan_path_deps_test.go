package agent

import (
	"slices"
	"testing"
)

// The ordering a planner did not declare, taken from the params.
//
// This is the live run it comes from: a bash step extracted two PDFs to
// extracted/pitch.txt and extracted/defence.txt, and two file_read steps opened
// those exact paths. depends_on was absent from all of them — the field had
// been moved out of the schema's required list and told the model it was
// "rarely needed" — so the three ran in one batch and both reads failed with
// "no such file or directory". A reflection and a replan were spent
// rediscovering it, and the second round did what the first should have.
func TestLinkPathDeps_OrdersAReadAfterTheWriteThatMakesIt(t *testing.T) {
	steps := []PlanStep{
		{Tag: "list_files", Tool: "file_list", Params: map[string]any{"path": "uploads/9ef2cf8c"}},
		{Tag: "extract_pdfs", Tool: "bash", Params: map[string]any{
			"command": "python3 -c 'import PyPDF2' && mkdir -p extracted && " +
				"python3 extract.py > extracted/pitch.txt && python3 extract.py > extracted/defence.txt"}},
		{Tag: "read_pitch", Tool: "file_read", Params: map[string]any{"path": "extracted/pitch.txt"}},
		{Tag: "read_defence", Tool: "file_read", Params: map[string]any{"path": "extracted/defence.txt"}},
	}
	linkPathDeps(steps)

	if !slices.Contains(steps[2].DependsOn, 1) {
		t.Errorf("read_pitch does not wait for extract_pdfs: deps=%v", steps[2].DependsOn)
	}
	if !slices.Contains(steps[3].DependsOn, 1) {
		t.Errorf("read_defence does not wait for extract_pdfs: deps=%v", steps[3].DependsOn)
	}
}

// A path inside a shell command counts. The run this comes from wrote its
// filenames into a bash command on one side and a path parameter on the other,
// so a check that only read path-shaped FIELDS would have seen nothing.
func TestLinkPathDeps_FindsAPathInsideACommand(t *testing.T) {
	steps := []PlanStep{
		{Tag: "write", Tool: "bash", Params: map[string]any{"command": "echo hi > out/report.md"}},
		{Tag: "read", Tool: "file_read", Params: map[string]any{"path": "./out/report.md"}},
	}
	linkPathDeps(steps)
	if !slices.Contains(steps[1].DependsOn, 0) {
		t.Errorf("a path written inside a command was not matched: deps=%v", steps[1].DependsOn)
	}
}

// Steps that share no file are left alone. Ordering two that did not need it
// costs parallelism, and this should not spend it where there is nothing.
func TestLinkPathDeps_LeavesUnrelatedStepsParallel(t *testing.T) {
	steps := []PlanStep{
		{Tag: "a", Tool: "web_search", Params: map[string]any{"query": "weather tokyo"}},
		{Tag: "b", Tool: "web_search", Params: map[string]any{"query": "weather delhi"}},
		{Tag: "c", Tool: "sysinfo", Params: map[string]any{}},
	}
	linkPathDeps(steps)
	for i, s := range steps {
		if len(s.DependsOn) != 0 {
			t.Errorf("step %d was ordered against nothing: deps=%v", i, s.DependsOn)
		}
	}
}

// A word is not a path, and a version is not a file. Treating either as one
// would order steps that merely mention the same noun.
func TestLinkPathDeps_DoesNotMatchWordsOrVersions(t *testing.T) {
	steps := []PlanStep{
		{Tag: "a", Tool: "bash", Params: map[string]any{"command": "echo deploying version 4.2 of the product"}},
		{Tag: "b", Tool: "bash", Params: map[string]any{"command": "echo the product is at version 4.2"}},
	}
	linkPathDeps(steps)
	if len(steps[1].DependsOn) != 0 {
		t.Errorf("two steps sharing a word were ordered: deps=%v", steps[1].DependsOn)
	}
}

// An ordering already declared is not declared twice.
func TestLinkPathDeps_DoesNotDuplicateADeclaredDependency(t *testing.T) {
	steps := []PlanStep{
		{Tag: "write", Tool: "bash", Params: map[string]any{"command": "make > build/out.log"}},
		{Tag: "read", Tool: "file_read", Params: map[string]any{"path": "build/out.log"}, DependsOn: FlexInts{0}},
	}
	linkPathDeps(steps)
	if len(steps[1].DependsOn) != 1 {
		t.Errorf("a declared dependency was added again: deps=%v", steps[1].DependsOn)
	}
}

// Only backwards. A step cannot wait for one that comes after it, and a plan
// must stay acyclic.
func TestLinkPathDeps_OnlyOrdersBackwards(t *testing.T) {
	steps := []PlanStep{
		{Tag: "read", Tool: "file_read", Params: map[string]any{"path": "data/in.csv"}},
		{Tag: "write", Tool: "bash", Params: map[string]any{"command": "cat data/in.csv > /dev/null"}},
	}
	linkPathDeps(steps)
	if len(steps[0].DependsOn) != 0 {
		t.Errorf("the earlier step was ordered after the later one: deps=%v", steps[0].DependsOn)
	}
	if !slices.Contains(steps[1].DependsOn, 0) {
		t.Errorf("the later step was not ordered after the earlier: deps=%v", steps[1].DependsOn)
	}
}
