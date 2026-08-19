package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/Compdeep/kaiju/agent/toolapi"
	"testing"
)

// Where a write may land is the application's rule, and this proves both answers to it.
//
// The rule used to be built in: every write went through SafeJoin against a workspace,
// which refuses absolute paths and anything outside five named subdirectories. An
// application operating on machines other than its own could not use the tool at all, so
// it wrote a second tool with the same name. What is checked here is that supplying no
// policy writes where told, and that the workspace policy still refuses exactly what it
// refused before — because that refusal is the reason a coder step could not overwrite
// cmd/kaiju/main.go again.

func writeWith(t *testing.T, where PathPolicy, path, content string, appendMode bool) (toolapi.ToolStatus, error) {
	t.Helper()
	msg, err := NewFileWrite(where).ExecuteTyped(context.Background(), map[string]any{
		"path": path, "content": content, "append": appendMode,
	})
	if err != nil {
		return "", err
	}
	return msg.Status, nil
}

// With no policy, a write lands at the absolute path it was given.
func TestNoPolicyWritesWhereItIsTold(t *testing.T) {
	target := filepath.Join(t.TempDir(), "sub", "report.txt")

	status, err := writeWith(t, nil, target, "written", false)
	if err != nil {
		t.Fatalf("a write with no policy was refused: %v", err)
	}
	if status != "ok" {
		t.Errorf("status %q, want ok", status)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("nothing was written to the path given: %v", err)
	}
	if string(got) != "written" {
		t.Errorf("the file holds %q, want %q", got, "written")
	}
}

// The workspace policy refuses an absolute path, a parent-directory escape, and the
// workspace root itself — the three refusals that were built into the tool.
func TestTheWorkspacePolicyStillRefusesWhatItAlwaysDid(t *testing.T) {
	ws := t.TempDir()
	where := ConfineToWorkspace(ws)

	for _, path := range []string{
		filepath.Join(t.TempDir(), "elsewhere.txt"), // absolute, outside
		"../escaped.txt",    // out through the parent
		"cmd/kaiju/main.go", // a directory the zones do not allow
		"loose.txt",         // at the workspace root
	} {
		if _, err := writeWith(t, where, path, "x", false); err == nil {
			t.Errorf("the workspace policy allowed a write to %q", path)
		}
	}

	// And an allowed subdirectory still works, so the policy is not refusing everything.
	if _, err := writeWith(t, where, "project/notes.txt", "kept", false); err != nil {
		t.Fatalf("a write inside an allowed subdirectory was refused: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(ws, "project", "notes.txt"))
	if err != nil || string(got) != "kept" {
		t.Errorf("the allowed write did not land: %q %v", got, err)
	}
}

// A refusal names the path, so whoever asked can see what was refused.
func TestARefusalSaysWhichPathWasRefused(t *testing.T) {
	_, err := writeWith(t, ConfineToWorkspace(t.TempDir()), "../escaped.txt", "x", false)
	if err == nil {
		t.Fatal("the escape was allowed")
	}
	if !strings.Contains(err.Error(), "file_write") {
		t.Errorf("the refusal does not say which tool refused: %v", err)
	}
}

// Appending twice keeps both writes, and reports appended.
//
// The append path closed the file with a deferred call, which threw away the close error.
// A close that fails after a write that did not means the bytes may never have reached the
// disk, and the tool reported success anyway.
func TestAppendingKeepsWhatWasThereAndReportsIt(t *testing.T) {
	target := filepath.Join(t.TempDir(), "log.txt")

	if _, err := writeWith(t, nil, target, "first\n", false); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := writeWith(t, nil, target, "second\n", true); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first\nsecond\n" {
		t.Errorf("the file holds %q, want both writes", got)
	}
}

// Writing without appending replaces what was there.
func TestWritingWithoutAppendingReplaces(t *testing.T) {
	target := filepath.Join(t.TempDir(), "conf.txt")

	writeWith(t, nil, target, "old and longer", false)
	if _, err := writeWith(t, nil, target, "new", false); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, _ := os.ReadFile(target)
	if string(got) != "new" {
		t.Errorf("the file holds %q — the previous contents were not replaced", got)
	}
}
