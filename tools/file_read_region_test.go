package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Reading a region, and reading it with line numbers.
//
// file_read could read the first N lines or the last N, and nothing else. A coder
// asked to change line 400 of a 2,000-line file had no way to see line 400, so its
// only option was to hold the whole file — and a whole-file rewrite from a file it
// could only partly see is how a server lost four of its five routes.
//
// The numbers are opt-in. A step that wires this text into a coder which writes the
// file back would otherwise save the numbers into the file.

func writeLines(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "long.txt")
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, params map[string]any) (string, fileReadData) {
	t.Helper()
	msg, err := (&FileRead{}).ExecuteTyped(nil, params)
	if err != nil {
		t.Fatalf("file_read: %v", err)
	}
	var d fileReadData
	if len(msg.Data) > 0 {
		if err := json.Unmarshal(msg.Data, &d); err != nil {
			t.Fatalf("payload: %v", err)
		}
	}
	return msg.Content, d
}

// A region in the middle of a file, which was unreachable before.
func TestFileRead_ReadsARegionInTheMiddle(t *testing.T) {
	path := writeLines(t, 2000)

	content, d := readFile(t, map[string]any{"path": path, "offset": 900, "max_lines": 5})

	if !strings.Contains(content, "line 900") || !strings.Contains(content, "line 904") {
		t.Errorf("the region was not returned:\n%s", content)
	}
	if strings.Contains(content, "line 899") || strings.Contains(content, "line 905") {
		t.Errorf("the region spilled past what was asked for:\n%s", content)
	}
	if d.FirstLine != 900 || d.LastLine != 904 {
		t.Errorf("the payload reports lines %d-%d, want 900-904", d.FirstLine, d.LastLine)
	}
	if d.LinesTotal != 2000 {
		t.Errorf("lines_total = %d, want 2000", d.LinesTotal)
	}
	// There is more of the file at both ends, and the reader has to know that.
	if !d.Truncated {
		t.Error("a region read of a longer file did not report that it was partial")
	}
}

// The numbers are the ones in the file, not the ones in the excerpt — an edit cites
// them back, so an off-by-the-offset number would edit the wrong place.
func TestFileRead_NumbersAreTheFilesOwn(t *testing.T) {
	path := writeLines(t, 500)

	content, _ := readFile(t, map[string]any{
		"path": path, "offset": 300, "max_lines": 3, "numbered": true,
	})

	for _, want := range []string{"300| line 300", "301| line 301", "302| line 302"} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q in:\n%s", want, content)
		}
	}
	if strings.Contains(content, "1| line 300") && !strings.Contains(content, "300| line 300") {
		t.Error("the lines were numbered from the excerpt rather than from the file")
	}
}

// Numbering is off unless asked for. This is the property that keeps the change safe
// for every existing caller: a coder handed numbered text that writes the file back
// would save the numbers into it.
func TestFileRead_IsNotNumberedByDefault(t *testing.T) {
	path := writeLines(t, 10)

	content, _ := readFile(t, map[string]any{"path": path, "max_lines": 3})

	if strings.Contains(content, "|") {
		t.Errorf("an unasked-for read came back numbered:\n%s", content)
	}
	if !strings.HasPrefix(content, "line 1\n") {
		t.Errorf("the text is not the file's own:\n%s", content)
	}
}

// A whole short file is not reported as partial, and carries its real range.
func TestFileRead_AWholeFileIsNotPartial(t *testing.T) {
	path := writeLines(t, 4)

	content, d := readFile(t, map[string]any{"path": path})

	if d.Truncated {
		t.Error("a file read in full was reported as truncated")
	}
	if strings.Contains(content, "showing lines") {
		t.Errorf("a full read carries a range note:\n%s", content)
	}
	if d.FirstLine != 1 || d.LastLine != 4 {
		t.Errorf("range is %d-%d, want 1-4", d.FirstLine, d.LastLine)
	}
}

// An offset past the end of the file returns nothing rather than the wrong lines.
func TestFileRead_OffsetPastTheEndReturnsNothing(t *testing.T) {
	path := writeLines(t, 10)

	content, d := readFile(t, map[string]any{"path": path, "offset": 99})

	if strings.Contains(content, "line ") {
		t.Errorf("lines were returned for a region that does not exist:\n%s", content)
	}
	if d.LinesShown != 0 {
		t.Errorf("lines_shown = %d, want 0", d.LinesShown)
	}
}

// tail_lines still wins and still behaves as it did.
func TestFileRead_TailIsUnchanged(t *testing.T) {
	path := writeLines(t, 100)

	content, d := readFile(t, map[string]any{"path": path, "tail_lines": 2, "offset": 50})

	if !strings.Contains(content, "line 100") {
		t.Errorf("tail did not return the end of the file:\n%s", content)
	}
	if !d.FromEnd {
		t.Error("from_end was not set for a tail read")
	}
}
