package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What the engine establishes about the file, so the coder is not guessing.
//
// No caller supplies it: edit_file passes seven keys, the architect eleven, a plan's
// compute step four, and none of them carries the file's contents. One coder was
// handed "Available Data: None", asked to preserve an Express server it had never
// seen, and wrote its own idea of one over the real thing.

func factsFor(t *testing.T, body string, lines int) (coderFileFacts, string) {
	t.Helper()
	ws := t.TempDir()
	// Deliberately outside the workspace: that is the case that used to fail.
	elsewhere := t.TempDir()
	path := filepath.Join(elsewhere, "main.ts")
	if body == "" {
		var sb strings.Builder
		for i := 1; i <= lines; i++ {
			fmt.Fprintf(&sb, "line %d\n", i)
		}
		body = sb.String()
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{cfg: Config{PathConfig: PathConfig{Workspace: ws}}}
	return a.coderFileFacts(nil, []string{path}, "tag"), path
}

// A whole short file arrives whole, numbered, and says so.
func TestCoderFileFacts_AShortFileArrivesWhole(t *testing.T) {
	facts, path := factsFor(t, "import express from \"express\";\napp.listen(5335);\n", 0)

	if !facts.Exists {
		t.Fatal("a readable file outside the workspace was reported as absent")
	}
	if facts.Lines != 2 || facts.Bytes == 0 {
		t.Errorf("lines=%d bytes=%d", facts.Lines, facts.Bytes)
	}
	if !facts.Whole {
		t.Error("a 2-line file was reported as partial")
	}

	out := facts.render()
	if !strings.Contains(out, "the whole file") {
		t.Errorf("the coder is not told it has all of it:\n%s", out)
	}
	if !strings.Contains(out, "     1| import express") || !strings.Contains(out, "     2| app.listen") {
		t.Errorf("the content is not numbered with the file's own numbers:\n%s", out)
	}
	if !strings.Contains(out, path) {
		t.Error("the block does not name the file")
	}
	if !strings.Contains(out, "Cite them in an edit's `lines`") {
		t.Error("nothing tells the coder the numbers are for citing")
	}
}

// A long file is cut, and the cut is stated along with what to do about it. A coder
// that rewrites a file it has only partly seen discards everything below the cut —
// which is how a server lost four of its five route mounts.
func TestCoderFileFacts_ALongFileSaysWhatIsMissing(t *testing.T) {
	facts, _ := factsFor(t, "", 1200)

	if facts.Lines != 1200 {
		t.Fatalf("lines=%d, want the file's true length", facts.Lines)
	}
	if facts.Whole {
		t.Fatal("a 1200-line file was reported as shown whole")
	}

	out := facts.render()
	if !strings.Contains(out, "lines 1-400") {
		t.Errorf("the shown range is not stated:\n%.400s", out)
	}
	if !strings.Contains(out, "800 lines you have NOT been shown") {
		t.Errorf("the unseen remainder is not counted:\n%.400s", out)
	}
	// The instruction that matters: do not rewrite what you cannot see.
	if !strings.Contains(out, "would discard everything below") {
		t.Errorf("nothing warns against rewriting a partly seen file:\n%.400s", out)
	}
	if !strings.Contains(out, "status blocked") {
		t.Errorf("the coder is not told it may ask for more:\n%.400s", out)
	}
	// The numbers stop at the cut and do not run past it.
	if strings.Contains(out, "   401|") {
		t.Error("content past the cut was included")
	}
}

// An absent file is a fact, not a silence. It is how creating is distinguished from
// editing, and it decides whether `edits` is offered at all.
func TestCoderFileFacts_AnAbsentFileSaysSo(t *testing.T) {
	ws := t.TempDir()
	a := &Agent{cfg: Config{PathConfig: PathConfig{Workspace: ws}}}

	facts := a.coderFileFacts(nil, []string{filepath.Join(t.TempDir(), "new.go")}, "tag")

	if facts.Exists {
		t.Fatal("a file that is not there was reported as present")
	}
	out := facts.render()
	if !strings.Contains(out, "does not exist yet") {
		t.Errorf("the coder is not told it is creating:\n%s", out)
	}
	if strings.Contains(out, "```") {
		t.Errorf("an absent file came with a content block:\n%s", out)
	}
}

// An empty file is nothing to edit, so it reads as a write.
func TestCoderFileFacts_AnEmptyFileIsNothingToEdit(t *testing.T) {
	facts, _ := factsFor(t, " ", 0)
	if facts.Exists && facts.Bytes == 0 {
		t.Error("a zero-byte file was reported as editable")
	}
}

// No task files, nothing to say.
func TestCoderFileFacts_NoTaskFilesRendersNothing(t *testing.T) {
	a := &Agent{cfg: Config{PathConfig: PathConfig{Workspace: t.TempDir()}}}
	if got := a.coderFileFacts(nil, nil, "tag").render(); got != "" {
		t.Errorf("a call with no task files produced a block:\n%s", got)
	}
}

// The facts are about the file that will be WRITTEN — the first one. editable used
// to come from whether any named file existed, while the edits applied to the first,
// so a multi-file list could offer replacements against a file never shown.
func TestCoderFileFacts_AreAboutTheFileThatGetsWritten(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.go") // does not exist
	second := filepath.Join(dir, "second.go")
	if err := os.WriteFile(second, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{cfg: Config{PathConfig: PathConfig{Workspace: t.TempDir()}}}

	facts := a.coderFileFacts(nil, []string{first, second}, "tag")

	if facts.Exists {
		t.Error("the facts describe a later task file rather than the one that gets written")
	}
	if facts.Path != first {
		t.Errorf("path = %s, want the first task file %s", facts.Path, first)
	}
}
