package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A directory it could only partly read still says what it read.
//
// du exits non-zero when it cannot read a subdirectory, having already printed
// the sizes it could read and a line per directory it could not. The tool
// discarded all of that and returned "disk_usage: exit status 1" — which is what
// asking about /tmp on a machine with anyone else's files produced, every time.
func TestDiskUsageKeepsWhatItCouldRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which can read anything")
	}
	root := t.TempDir()
	readable := filepath.Join(root, "readable")
	if err := os.MkdirAll(readable, 0o755); err != nil {
		t.Fatal(err)
	}
	// Something with size in it, so there is a listing to lose.
	if err := os.WriteFile(filepath.Join(readable, "big"), make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	closed := filepath.Join(root, "closed")
	if err := os.MkdirAll(filepath.Join(closed, "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(closed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o700) })

	msg, err := NewDiskUsage().ExecuteTyped(context.Background(), map[string]any{"path": root})
	if err != nil {
		t.Fatalf("returned a Go error rather than a partial answer: %v", err)
	}
	if msg.Content == "" {
		t.Fatal("the listing du printed was discarded, leaving the exit status as the whole answer")
	}
	if !strings.Contains(msg.Content, "readable") {
		t.Errorf("the part it could read is missing from the answer: %q", msg.Content)
	}
	if msg.Status != toolapi.StatusError {
		t.Errorf("status = %q; the answer is incomplete and should say so", msg.Status)
	}
	if !strings.Contains(msg.Detail, "could not read every directory") {
		t.Errorf("the detail does not say why it is incomplete: %q", msg.Detail)
	}
}
