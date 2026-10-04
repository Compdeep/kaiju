package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ApplyEdits applies replacements in order to the running result. These pin the
// cases where that is subtle: an ambiguous target, an earlier edit moving a later
// one's text, and the exactness of the match.

// Text occurring more than once is refused, not resolved by position.
//
// It used to take the first occurrence and report success, so an edit meaning the
// second changed the wrong line and said it had worked. The coder is the one that
// knows which it meant, and it can now say so — with lines, or with a longer
// excerpt that appears once.
func TestApplyEdits_AmbiguousMatchIsRefused(t *testing.T) {
	content := "timeout = 30\nretries = 3\ntimeout = 30\n"

	got, err := ApplyEdits(content, []EditOp{{OldContent: "timeout = 30", NewContent: "timeout = 60"}})
	if err == nil {
		t.Fatalf("an ambiguous edit was applied anyway, giving %q", got)
	}
	// The message has to carry the count and say what to do about it, or the coder
	// retries the same edit.
	if !strings.Contains(err.Error(), "appears 2 times") {
		t.Errorf("the error does not say how many matches there were: %v", err)
	}
	if !strings.Contains(err.Error(), "lines") {
		t.Errorf("the error does not say that lines would resolve it: %v", err)
	}
}

// Naming the lines resolves it: the same text, the second occurrence, chosen.
func TestApplyEdits_LinesChooseWhichOccurrence(t *testing.T) {
	content := "timeout = 30\nretries = 3\ntimeout = 30\n"

	got, err := ApplyEdits(content, []EditOp{
		{Lines: []int{3, 3}, OldContent: "timeout = 30", NewContent: "timeout = 60"},
	})
	if err != nil {
		t.Fatalf("a line-named edit was refused: %v", err)
	}
	if got != "timeout = 30\nretries = 3\ntimeout = 60\n" {
		t.Errorf("the wrong occurrence was replaced: %q", got)
	}
}

// The text is the check on the lines, and when it is somewhere else the error says
// WHERE. "The text is at line 3, not 1" is actionable; "expected X, found Y" reads
// as the file being wrong and invites the same edit again — which is what a coder
// did, retrying a correct edit twice.
func TestApplyEdits_SaysWhereTheTextActuallyIs(t *testing.T) {
	content := "alpha\nbeta\ngamma\n"

	_, err := ApplyEdits(content, []EditOp{
		{Lines: []int{1, 1}, OldContent: "gamma", NewContent: "delta"},
	})
	if err == nil {
		t.Fatal("an edit whose text is not at the lines it named was applied")
	}
	if !strings.Contains(err.Error(), "at line 3") {
		t.Errorf("the error does not say where the text is: %v", err)
	}
	if !strings.Contains(err.Error(), "1-1") {
		t.Errorf("the error does not name the lines that were cited: %v", err)
	}
}

// Text that is nowhere in the file still names both sides, because there is no
// "where it actually is" to give.
func TestApplyEdits_TextNowhereInTheFileNamesBothSides(t *testing.T) {
	_, err := ApplyEdits("alpha\nbeta\n", []EditOp{
		{Lines: []int{1, 1}, OldContent: "nothing like this", NewContent: "x"},
	})
	if err == nil {
		t.Fatal("an edit for text that is not in the file was applied")
	}
	if !strings.Contains(err.Error(), "expected") || !strings.Contains(err.Error(), "found") {
		t.Errorf("the error does not carry both sides: %v", err)
	}
	if !strings.Contains(err.Error(), "nowhere in the file") {
		t.Errorf("the error does not say the text is absent entirely: %v", err)
	}
}

// Lines past the end of the file say so, rather than reading as "not found".
func TestApplyEdits_LinesPastTheEndSaySo(t *testing.T) {
	_, err := ApplyEdits("one\ntwo\n", []EditOp{
		{Lines: []int{40, 41}, OldContent: "two", NewContent: "three"},
	})
	if err == nil {
		t.Fatal("lines past the end of the file were accepted")
	}
	if !strings.Contains(err.Error(), "past the end") {
		t.Errorf("the error does not say the range is past the end: %v", err)
	}
}

// An identical string outside the named range is untouched — the replacement
// happens within the span, not across the file.
func TestApplyEdits_LeavesIdenticalTextOutsideTheRange(t *testing.T) {
	content := "x = 1\nx = 1\nx = 1\n"

	got, err := ApplyEdits(content, []EditOp{
		{Lines: []int{2, 2}, OldContent: "x = 1", NewContent: "x = 2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "x = 1\nx = 2\nx = 1\n" {
		t.Errorf("the edit reached outside its range: %q", got)
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

	changed, err := ApplyFileEdits(path, []EditOp{
		{OldContent: "port = 8080", NewContent: "port = 9090"},
		{OldContent: "this text is not in the file", NewContent: "x"},
	})
	if err == nil {
		t.Fatal("the second edit cannot apply, so the call must fail")
	}
	if changed {
		t.Error("a failed edit reported that it changed the file")
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if string(after) != original {
		t.Fatalf("a failed edit must not write a partial file, got %q", string(after))
	}
}

// An earlier edit growing the file must not invalidate a later edit's lines.
//
// This is the set that failed in a real run. Edit 1 replaced 12 lines with 14 at
// line 61; edit 2 named line 122, correct for the file the coder was shown and two
// lines stale by the time it was checked against the partly-edited result. It was
// refused, the coder was told the file was not what it thought, and it retried the
// same correct edit twice.
//
// Positions are resolved against the original and applied bottom-up, so this holds
// however many lines an earlier edit adds or removes.
func TestApplyEdits_AnEarlierEditDoesNotMoveALaterOnesLines(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	content := sb.String()

	got, err := ApplyEdits(content, []EditOp{
		// Grows by two lines, shifting everything below it.
		{Lines: []int{61, 61}, OldContent: "line 61", NewContent: "line 61\nadded A\nadded B"},
		// Named against the original, far below the growth.
		{Lines: []int{122, 122}, OldContent: "line 122", NewContent: "line 122 CHANGED"},
		// And one above it, which nothing moves.
		{Lines: []int{10, 10}, OldContent: "line 10", NewContent: "line 10 CHANGED"},
	})
	if err != nil {
		t.Fatalf("a set whose earlier edit grew the file was refused: %v", err)
	}
	for _, want := range []string{"line 10 CHANGED", "added A", "added B", "line 122 CHANGED"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q from the result", want)
		}
	}
	// The one below the growth landed on its own line and not on a neighbour.
	lines := strings.Split(got, "\n")
	for i, l := range lines {
		if l == "line 122 CHANGED" {
			// Original 122, plus the two lines edit 1 added above it.
			if i+1 != 124 {
				t.Errorf("the edit landed at line %d, want 124 after two lines were added above it", i+1)
			}
		}
	}
	if strings.Contains(got, "line 124 CHANGED") {
		t.Error("the edit changed the line that moved INTO position 122 rather than the one it named")
	}
}

// Shrinking is the same problem in the other direction.
func TestApplyEdits_AnEarlierEditThatShrinksTheFileIsAlsoSafe(t *testing.T) {
	content := "a\nb\nc\nd\ne\nf\n"

	got, err := ApplyEdits(content, []EditOp{
		{Lines: []int{2, 4}, OldContent: "b\nc\nd", NewContent: "bcd"}, // 3 lines -> 1
		{Lines: []int{6, 6}, OldContent: "f", NewContent: "F"},
	})
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if got != "a\nbcd\ne\nF\n" {
		t.Errorf("got %q, want the later edit to have landed on f", got)
	}
}

// Two edits to the same lines is refused rather than resolved by order — whichever
// applied second would be checked against text the first had changed, which is the
// failure this design removes.
func TestApplyEdits_OverlappingRangesAreRefused(t *testing.T) {
	_, err := ApplyEdits("a\nb\nc\nd\n", []EditOp{
		{Lines: []int{1, 3}, OldContent: "a", NewContent: "A"},
		{Lines: []int{2, 4}, OldContent: "c", NewContent: "C"},
	})
	if err == nil {
		t.Fatal("two edits covering the same lines were accepted")
	}
	if !strings.Contains(err.Error(), "same lines") {
		t.Errorf("the error does not explain the overlap: %v", err)
	}
	// Both are named, by the index the coder wrote them at.
	if !strings.Contains(err.Error(), "edits 1 and 2") {
		t.Errorf("the error does not name both edits: %v", err)
	}
}

// Nothing is applied when any edit in the set cannot be. Checked against the
// original up front, so the error describes the real problem rather than a
// consequence of a partial application.
func TestApplyEdits_AWholeSetFailsBeforeAnyOfItApplies(t *testing.T) {
	content := "keep\nchange me\nkeep\n"

	got, err := ApplyEdits(content, []EditOp{
		{Lines: []int{2, 2}, OldContent: "change me", NewContent: "changed"},
		{Lines: []int{3, 3}, OldContent: "not there", NewContent: "x"},
	})
	if err == nil {
		t.Fatal("a set with an impossible edit was applied")
	}
	if got != "" {
		t.Errorf("a failed set returned content: %q", got)
	}
	// The error is about edit 2, not a mismatch caused by edit 1 having run.
	if !strings.Contains(err.Error(), "edit 2") {
		t.Errorf("the error does not name the edit that could not apply: %v", err)
	}
}

// Positional and textual edits in one set: positions are resolved against the
// original, then the text edits run on the result.
func TestApplyEdits_MixesPositionalAndTextual(t *testing.T) {
	content := "alpha\nbeta\ngamma\n"

	got, err := ApplyEdits(content, []EditOp{
		{Lines: []int{1, 1}, OldContent: "alpha", NewContent: "ALPHA\nextra"},
		{OldContent: "gamma", NewContent: "GAMMA"},
	})
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if got != "ALPHA\nextra\nbeta\nGAMMA\n" {
		t.Errorf("got %q", got)
	}
}
