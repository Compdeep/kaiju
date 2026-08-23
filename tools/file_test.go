package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Reading the end of a file is a different question from reading the start, and
// max_lines cannot express it: the interesting part of a log is at the bottom,
// and truncating from the top throws it away.
func TestFileRead_TailLines(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.log"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	msg, err := NewFileRead(dir).ExecuteTyped(context.Background(),
		map[string]any{"path": "app.log", "tail_lines": 3})
	if err != nil {
		t.Fatalf("file_read: %v", err)
	}
	if !strings.Contains(msg.Content, "line 20") {
		t.Errorf("the last line is missing, which is the whole point:\n%s", msg.Content)
	}
	if strings.Contains(msg.Content, "line 1\n") {
		t.Errorf("the start of the file came back too:\n%s", msg.Content)
	}

	// And the head is still the default.
	msg, err = NewFileRead(dir).ExecuteTyped(context.Background(),
		map[string]any{"path": "app.log", "max_lines": 3})
	if err != nil {
		t.Fatalf("file_read: %v", err)
	}
	if !strings.Contains(msg.Content, "line 1") || strings.Contains(msg.Content, "line 20") {
		t.Errorf("max_lines should read from the start:\n%s", msg.Content)
	}
}

// Reading a log must not cost the log's size in memory.
//
// This read the whole file and then threw away all but a few lines, so asking
// for the last five lines of a 200MB log needed 200MB of heap plus the split.
// A log is exactly what this tool gets pointed at, and an agent reading one on
// someone else's machine should not need the file in memory to do it.
func TestFileRead_DoesNotHoldTheWholeFile(t *testing.T) {
	dir := t.TempDir()
	fh, err := os.Create(filepath.Join(dir, "big.log"))
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(fh)
	line := strings.Repeat("x", 99) + "\n"
	for i := 0; i < 500_000; i++ { // ~50MB, written incrementally
		if _, err := w.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush()
	fh.Close()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	msg, err := NewFileRead(dir).ExecuteTyped(context.Background(),
		map[string]any{"path": "big.log", "tail_lines": 5})
	if err != nil {
		t.Fatalf("file_read: %v", err)
	}
	runtime.ReadMemStats(&after)

	if !strings.Contains(msg.Content, "showing the last 5") {
		t.Errorf("the tail was not applied:\n%s", msg.Content)
	}
	// Generous: the point is that it is bounded by the lines asked for, not by
	// the file. Holding the file would put this above 50.
	if mb := after.HeapAlloc / 1024 / 1024; mb > 20 {
		t.Errorf("reading 5 lines of a 50MB file left %d MB on the heap — "+
			"the file is being held in memory", mb)
	}
}
