package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file no model can read is described, not read.
//
// The planner points file_read at whatever a step names, and a step that names
// a program gets one: /usr/bin/apt-get and /bin/sh were both read whole into a
// prompt on a live deployment, where 13.5% of every byte ever sent to a model
// was executable content.
func TestFileReadDescribesBinariesRatherThanReadingThem(t *testing.T) {
	dir := t.TempDir()
	elf := filepath.Join(dir, "prog")
	body := append([]byte("\x7fELF\x02\x01\x01\x00"), make([]byte, 4096)...)
	if err := os.WriteFile(elf, body, 0o644); err != nil {
		t.Fatal(err)
	}

	msg, err := NewFileRead("").ExecuteTyped(context.Background(), map[string]any{"path": elf})
	if err != nil {
		t.Fatalf("reading a binary should not fail the step: %v", err)
	}
	if strings.Contains(msg.Content, "\x7fELF") || strings.ContainsRune(msg.Content, 0) {
		t.Error("the binary's own bytes reached the result")
	}
	for _, want := range []string{"ELF", "not text"} {
		if !strings.Contains(msg.Content, want) {
			t.Errorf("the result does not say %q: %q", want, msg.Content)
		}
	}
	// Reported as OK, because nothing went wrong — the step asked what is in
	// this file and got an answer. A failure would send the run looking for a
	// fault, and the retry would read the same bytes again.
	if msg.Status != "ok" {
		t.Errorf("status = %q, want ok", msg.Status)
	}
}

// Text is unaffected, including text that merely looks unusual.
func TestFileReadStillReadsText(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"log.txt":  "line one\nline two\nline three\n",
		"odd.txt":  "MZ is how a PE file starts, but this is prose about it.\n",
		"empty.md": "",
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		msg, err := NewFileRead("").ExecuteTyped(context.Background(), map[string]any{"path": p})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(msg.Content, "not text") {
			t.Errorf("%s was refused as binary: %q", name, msg.Content)
		}
	}
}
