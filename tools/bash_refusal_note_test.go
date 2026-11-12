package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A command that failed on its own, such as ls of a file that does not exist,
// says why in its own words, and not what the step's level allows: the level
// had nothing to do with it, and the sentence read as a refusal.
func TestBash_AnOrdinaryFailureSaysWhyNotTheLevel(t *testing.T) {
	ctx := toolapi.WithLevel(context.Background(), toolapi.ImpactObserve)
	got, _ := NewBash("").ExecuteTyped(ctx, map[string]any{"command": "ls /no/such/file"})
	if strings.Contains(got.Detail, "runs at level") {
		t.Errorf("the level was named for a failure it did not cause: %s", got.Detail)
	}
	if !strings.Contains(got.Detail, "No such file") {
		t.Errorf("the command's own reason is missing: %s", got.Detail)
	}
}

// A command the level refused still says so, so it is not retried as though
// it might work the next time.
func TestBash_ARefusalNamesTheLevel(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the lock is Linux's")
	}
	dir := t.TempDir()
	ctx := toolapi.WithLevel(context.Background(), toolapi.ImpactObserve)
	got, _ := NewBash("").ExecuteTyped(ctx, map[string]any{"command": "touch " + filepath.Join(dir, "f")})
	if _, err := os.Stat(filepath.Join(dir, "f")); err == nil {
		t.Skip("this kernel did not lock the command")
	}
	if !strings.Contains(got.Detail, "runs at level") {
		t.Errorf("a refused command does not name the level: %s", got.Detail)
	}
}
