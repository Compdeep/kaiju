package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ApplyEdits replaces the FIRST occurrence of old_content and reports success.
// These pin the cases where that is silently the wrong occurrence, or where an
// edit reports success having changed nothing. They record what the code does
// today so a change to it is deliberate — two of them describe behaviour worth
// changing, and say so.

// HAZARD. old_content occurring more than once is not an error: the first is
// replaced and the caller is told the edit succeeded. Whoever wrote the edit may
// have meant the second, and nothing reports the ambiguity. Claude Code's Edit
// tool refuses this case for that reason.
func TestApplyEdits_AmbiguousMatchTakesTheFirstAndSaysNothing(t *testing.T) {
	content := "timeout = 30\nretries = 3\ntimeout = 30\n"
	got, err := ApplyEdits(content, []EditOp{{OldContent: "timeout = 30", NewContent: "timeout = 60"}})
	if err != nil {
		t.Fatalf("today this is accepted, not refused: %v", err)
	}
	if got != "timeout = 60\nretries = 3\ntimeout = 30\n" {
		t.Fatalf("the first occurrence is the one replaced, got %q", got)
	}
	if strings.Count(got, "timeout = 30") != 1 {
		t.Fatal("the second occurrence must be left, which is the hazard: it may have been the intended one")
	}
}

// HAZARD. Edits apply in sequence to the running result, so an earlier edit can
// remove the text a later one anchors to. The later edit then fails, and the
// file is left with the earlier edit applied — a partial write reported as a
// whole failure.
func TestApplyEdits_AnEarlierEditCanDestroyALaterAnchor(t *testing.T) {
	content := "a = 1\nb = 2\n"
	_, err := ApplyEdits(content, []EditOp{
		{OldContent: "a = 1\nb = 2", NewContent: "a = 1"},
		{OldContent: "b = 2", NewContent: "b = 3"},
	})
	if err == nil {
		t.Fatal("the second edit's anchor was removed by the first, so it cannot apply")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("the failure should name the missing anchor, got %v", err)
	}
}

// An edit whose new content equals its old content succeeds and changes nothing.
// Harmless in itself, but it means "the edit applied" does not imply "the file
// changed", so a caller cannot infer one from the other.
func TestApplyEdits_ANoOpEditReportsSuccess(t *testing.T) {
	content := "unchanged\n"
	got, err := ApplyEdits(content, []EditOp{{OldContent: "unchanged", NewContent: "unchanged"}})
	if err != nil || got != content {
		t.Fatalf("a no-op edit succeeds and leaves the content alone, got %q err %v", got, err)
	}
}

// Matching is byte-exact, so a file with Windows line endings does not match
// old_content quoted with Unix ones. Correct, and worth pinning: a fixture saved
// with CRLF would fail every multi-line edit for a reason nothing states.
func TestApplyEdits_LineEndingsMustMatchExactly(t *testing.T) {
	crlf := "first\r\nsecond\r\n"
	if _, err := ApplyEdits(crlf, []EditOp{{OldContent: "first\nsecond", NewContent: "x"}}); err == nil {
		t.Fatal("a unix-quoted anchor must not match a CRLF file")
	}
	got, err := ApplyEdits(crlf, []EditOp{{OldContent: "first\r\nsecond", NewContent: "x"}})
	if err != nil || got != "x\r\n" {
		t.Fatalf("the same anchor with matching endings applies, got %q err %v", got, err)
	}
}

// Text outside the Basic Latin range is replaced like any other, since matching
// is on bytes rather than on anything language-aware.
func TestApplyEdits_HandlesTextBeyondBasicLatin(t *testing.T) {
	content := "greeting = \"héllo wörld\"\nname = \"日本語\"\n"
	got, err := ApplyEdits(content, []EditOp{
		{OldContent: `"héllo wörld"`, NewContent: `"goodbye"`},
		{OldContent: `"日本語"`, NewContent: `"english"`},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got != "greeting = \"goodbye\"\nname = \"english\"\n" {
		t.Fatalf("got %q", got)
	}
}

// ApplyFileEdits leaves the file untouched when an edit cannot apply, so a
// failed edit does not produce a half-written file on disk.
func TestApplyFileEdits_LeavesTheFileAloneWhenAnEditCannotApply(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.conf")
	original := "port = 8080\nhost = localhost\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	err := ApplyFileEdits(path, []EditOp{
		{OldContent: "port = 8080", NewContent: "port = 9090"},
		{OldContent: "this text is not in the file", NewContent: "x"},
	})
	if err == nil {
		t.Fatal("the second edit cannot apply, so the call must fail")
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if string(after) != original {
		t.Fatalf("a failed edit must not write a partial file, got %q", string(after))
	}
}
