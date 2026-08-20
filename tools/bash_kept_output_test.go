package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// What comes back from a command inline is a few kilobytes, because it travels
// into a prompt. Everything it printed used to stop there — a command that
// printed ten thousand lines reported its first couple of hundred and the run
// reasoned from those. Now the rest is on disk and the result says where.

func bashPayload(t *testing.T, msg toolapi.ToolMessage) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(msg.Data, &out); err != nil {
		t.Fatalf("payload is not readable: %v", err)
	}
	return out
}

func TestBash_KeepsOutputPastWhatItReturnsInline(t *testing.T) {
	ws := t.TempDir()
	b := NewBash("sh", ws)

	// More than the inline cut, by a lot, with a marker at the very end so the
	// test can tell a whole file from a truncated one.
	script := `for i in $(seq 1 4000); do echo "line $i ................................................"; done; echo THE_LAST_LINE`
	msg, err := b.ExecuteTyped(context.Background(), map[string]any{"command": script})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if len(msg.Content) > 9000 {
		t.Errorf("inline content is %d bytes; it is supposed to stay small", len(msg.Content))
	}
	if strings.Contains(msg.Content, "THE_LAST_LINE") {
		t.Fatal("the whole output came back inline, so this test is not exercising the cut")
	}

	p := bashPayload(t, msg)
	rel, _ := p["output_path"].(string)
	if rel == "" {
		t.Fatal("no output_path, so everything past the inline cut is gone")
	}

	kept, rerr := os.ReadFile(filepath.Join(ws, rel))
	if rerr != nil {
		t.Fatalf("output_path does not lead to the output: %v", rerr)
	}
	if !strings.Contains(string(kept), "THE_LAST_LINE") {
		t.Error("the kept file does not hold the end of the output, so it is cut too")
	}
	if !strings.Contains(string(kept), "line 1 ") {
		t.Error("the kept file does not hold the start of the output")
	}
	if n, _ := p["output_bytes"].(float64); int(n) != len(kept) {
		t.Errorf("output_bytes = %v, file is %d", p["output_bytes"], len(kept))
	}
}

// A command that fails is the case that matters most: its output is the
// evidence for why, and the reason is usually at the end.
func TestBash_KeepsOutputWhenTheCommandFails(t *testing.T) {
	ws := t.TempDir()
	b := NewBash("sh", ws)

	msg, err := b.ExecuteTyped(context.Background(), map[string]any{
		"command": `echo "some output"; echo "the reason it failed" >&2; exit 3`,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if msg.Status != toolapi.StatusError {
		t.Fatalf("status = %q, want error", msg.Status)
	}

	p := bashPayload(t, msg)
	rel, _ := p["output_path"].(string)
	if rel == "" {
		t.Fatal("a failed command kept nothing, so why it failed is only in the inline cut")
	}
	kept, rerr := os.ReadFile(filepath.Join(ws, rel))
	if rerr != nil {
		t.Fatalf("read: %v", rerr)
	}
	if !strings.Contains(string(kept), "the reason it failed") {
		t.Error("the kept output does not include stderr, which is where a failure says why")
	}
}

// A tool with no working directory is one running on a machine it does not own.
// It writes nothing there, and says nothing about a path it did not create.
func TestBash_WithNoWorkingDirectoryWritesNothing(t *testing.T) {
	b := NewBash("sh")
	msg, err := b.ExecuteTyped(context.Background(), map[string]any{"command": `echo hello`})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if p := bashPayload(t, msg); p["output_path"] != nil {
		t.Errorf("a sandbox-less bash reported a path: %v", p["output_path"])
	}
}
