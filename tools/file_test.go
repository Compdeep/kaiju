package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
