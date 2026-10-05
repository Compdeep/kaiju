package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The request the coder is given, as one declared thing.
//
// edit_file passes seven keys, the architect eleven, a plan's compute step four,
// and the coder could rely on none of them being present. One call was handed
// "Available Data: None", asked to preserve an Express server it had never seen,
// and wrote its own idea of one over the real thing.

// Every section a caller can contribute reaches the prompt. This is the test that
// would have caught the field that was declared and never filled.
func TestCoderRequest_RenderCarriesEverySectionACallerFills(t *testing.T) {
	req := CoderRequest{
		File:       "/app/main.ts",
		Brief:      "keep the existing route mounts",
		Interfaces: map[string]any{"Objective": "id, title"},
		Structure:  "app/\n  main.ts",
		Files:      "\n## Your Task Files (write ONLY these)\n- /app/main.ts\n",
		Prior:      []string{"[12:00] edit_routes — EDIT: /app/main.ts — 2 edits applied: added /api/objectives"},
	}
	out := req.render()

	for _, want := range []string{
		"keep the existing route mounts", // brief
		"Objective",                      // interfaces
		"app/\n  main.ts",                // structure
		"/app/main.ts",                   // the files block
		"added /api/objectives",          // prior
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the prompt does not carry %q:\n%s", want, out)
		}
	}
	// Prior has to say what it is, or a coder reads it as work still to do.
	if !strings.Contains(out, "Already done to /app/main.ts") {
		t.Error("the prior section does not say these edits are already in the file")
	}
	if !strings.Contains(out, "Do not make them again") {
		t.Error("nothing tells the coder not to repeat the edits it is shown")
	}
}

// An empty request renders nothing rather than a run of empty headings.
func TestCoderRequest_NothingToSayRendersNothing(t *testing.T) {
	if got := (CoderRequest{}).render(); got != "" {
		t.Errorf("an empty request produced:\n%s", got)
	}
}

// A file with no history gets no section, so the coder is not shown an empty one
// and left to wonder whether that means untouched or unknown.
func TestCoderRequest_NoPriorSectionWhenTheFileIsUntouched(t *testing.T) {
	out := CoderRequest{File: "/app/new.ts", Files: "x"}.render()
	if strings.Contains(out, "Already done") {
		t.Errorf("an untouched file was given a history section:\n%s", out)
	}
}

// priorEditsFor reads the worklog and returns only this file's coder lines.
func TestPriorEditsFor_OnlyThisFileAndOnlyCoderLines(t *testing.T) {
	dir := t.TempDir()
	session := "s1"
	if err := os.MkdirAll(filepath.Join(dir, "sessions", session), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		"[12:00] edit_home — EDIT: /app/Home.vue — 2 edits applied: split the hero copy",
		"[12:01] edit_other — EDIT: /app/Other.vue — 1 edits applied: renamed a prop",
		"[12:02] read_home — OK: file_read: /app/Home.vue contents here",
		"[12:03] run_build — OK: bash: npm run build touched /app/Home.vue",
		"[12:04] retry_home — NO_CHANGE: 1 edit(s) applied to /app/Home.vue and left it as it was",
		"[12:05] blocked_home — BLOCKED: cannot preserve /app/Home.vue without seeing it",
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions", session, "worklog"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{cfg: Config{PathConfig: PathConfig{MetadataDir: dir}}}

	got := a.priorEditsFor(session, "/app/Home.vue")

	if len(got) != 3 {
		t.Fatalf("got %d lines, want the EDIT, NO_CHANGE and BLOCKED for this file:\n%s",
			len(got), strings.Join(got, "\n"))
	}
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "Other.vue") {
		t.Error("another file's history leaked in")
	}
	if strings.Contains(joined, "file_read") || strings.Contains(joined, "npm run build") {
		t.Error("a read and a bash command that merely named the path were counted as edits")
	}
	if !strings.Contains(joined, "split the hero copy") {
		t.Error("the coder's own summary is not carried, so prior says work happened and not what it was")
	}
}

// No worklog, no history, no error.
func TestPriorEditsFor_NoWorklogIsNotAFailure(t *testing.T) {
	a := &Agent{cfg: Config{PathConfig: PathConfig{MetadataDir: t.TempDir()}}}
	if got := a.priorEditsFor("nope", "/app/x.ts"); got != nil {
		t.Errorf("got %v, want nothing", got)
	}
	if got := a.priorEditsFor("nope", ""); got != nil {
		t.Errorf("an empty path returned %v", got)
	}
}

// Bounded, because a session that returns to one file many times would otherwise
// push the file itself out of the prompt.
func TestPriorEditsFor_IsBoundedToTheRecentOnes(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions", "s"), 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, "[12:00] t — EDIT: /app/x.ts — 1 edits applied: pass")
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions", "s", "worklog"),
		[]byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{cfg: Config{PathConfig: PathConfig{MetadataDir: dir}}}

	if got := a.priorEditsFor("s", "/app/x.ts"); len(got) != coderPriorLines {
		t.Errorf("got %d lines, want %d", len(got), coderPriorLines)
	}
}
