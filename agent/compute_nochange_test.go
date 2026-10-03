package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A write that changes nothing must not report that it wrote.
//
// 14-s11.html was rewritten four times by a coder that handed the file back
// essentially unchanged. Each pass logged "OK: wrote …", the next coder read that as
// the work being done, and after four rounds 32 em-dashes had become 31. The
// reflector read four successes and concluded all fourteen sections were edited.
//
// Nothing in the engine compared a write to what was already there, so a no-op and
// real work were the same event.

// ── the edit path ───────────────────────────────────────────────────────────

// Edits that apply cleanly and leave the text as it was report changed=false.
//
// "N edits applied" counts operations, not effect. Four replacements that each swap
// text for identical text is still four applied edits and no error.
func TestApplyFileEdits_ReportsWhenNothingChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "section.html")
	const original = "<p>a claim is admitted — or it is not</p>\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	// A replacement whose new text equals its old text.
	changed, err := ApplyFileEdits(path, []EditOp{
		{OldContent: "a claim is admitted", NewContent: "a claim is admitted"},
	})
	if err != nil {
		t.Fatalf("the edit applied, so there is no error to report: %v", err)
	}
	if changed {
		t.Error("an edit that left the text as it was reported that it changed the file")
	}
	after, _ := os.ReadFile(path)
	if string(after) != original {
		t.Errorf("the file was altered: %q", after)
	}
}

// A real edit still reports changed=true and still writes.
func TestApplyFileEdits_ReportsARealChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "section.html")
	if err := os.WriteFile(path, []byte("port = 8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := ApplyFileEdits(path, []EditOp{
		{OldContent: "8080", NewContent: "9090"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("a replacement that altered the text reported no change")
	}
	after, _ := os.ReadFile(path)
	if string(after) != "port = 9090\n" {
		t.Errorf("the edit did not reach disk: %q", after)
	}
}

// An unchanged result is not written at all, so the file's modification time does
// not move. A rewrite that touches the file looks like work to everything that
// watches the filesystem — a build, a watcher, a git status.
func TestApplyFileEdits_DoesNotTouchTheFileWhenNothingChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "section.html")
	if err := os.WriteFile(path, []byte("unchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ApplyFileEdits(path, []EditOp{{OldContent: "unchanged", NewContent: "unchanged"}}); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("the file was rewritten with identical content, moving its modification time")
	}
}

// ── what a no-op reports ────────────────────────────────────────────────────

// The result carries no_changes, which computebody already turns into an empty
// envelope rather than an OK one — that is how a stage downstream gets a verdict
// other than success without anything else changing.
func TestComputeNoChange_ReportsNoChangesAndTheReason(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{cfg: Config{PathConfig: PathConfig{Workspace: dir, MetadataDir: dir}}}
	g := &Graph{SessionID: "s1"}

	raw, err := a.computeNoChange(g, "edit_s11", "docs/src/sections/14-s11.html",
		"the coder returned docs/src/sections/14-s11.html unchanged")
	if err != nil {
		t.Fatal(err)
	}

	var got struct {
		Type      string `json:"type"`
		NoChanges bool   `json:"no_changes"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("the result is not the shape computebody reads: %v\n%s", err, raw)
	}
	if !got.NoChanges {
		t.Error("no_changes is not set, so this reads as an ordinary result")
	}
	if !strings.Contains(got.Reason, "unchanged") {
		t.Errorf("the reason does not say what happened: %q", got.Reason)
	}
	// The reason has to name the file. A stage reading "unchanged" with no path
	// cannot tell which of fourteen steps did nothing.
	if !strings.Contains(got.Reason, "14-s11.html") {
		t.Errorf("the reason does not name the file: %q", got.Reason)
	}
}

// The worklog line says NO_CHANGE, not OK.
//
// This is the line the next coder reads. "OK: wrote …" is what told four passes in
// a row that the job was already done.
func TestComputeNoChange_WritesNoChangeToTheWorklog(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{cfg: Config{PathConfig: PathConfig{Workspace: dir, MetadataDir: dir}}}
	g := &Graph{SessionID: "s1"}

	if _, err := a.computeNoChange(g, "edit_s11", "14-s11.html", "the coder returned 14-s11.html unchanged"); err != nil {
		t.Fatal(err)
	}

	wl := readWorklog(dir, "s1", 10)
	if wl == "" {
		t.Fatal("nothing was written to the worklog")
	}
	if !strings.Contains(wl, "NO_CHANGE") {
		t.Errorf("the worklog does not carry the verdict:\n%s", wl)
	}
	// And it must not read as a success, which is the whole point.
	if strings.Contains(wl, "— OK:") {
		t.Errorf("a no-op was logged as OK:\n%s", wl)
	}
	if !strings.Contains(wl, "edit_s11") {
		t.Errorf("the line is not attributed to the step:\n%s", wl)
	}
}
