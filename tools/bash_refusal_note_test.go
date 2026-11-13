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

// A command that failed with nothing on stderr, because it sent its errors to
// /dev/null, says that the cause is not known, and names the level as one
// possible cause. Said with nothing, "exit 1" and an empty output read to every
// stage as "nothing there": run 19:40 2026-11-12 reported google_sudoers as
// empty and no SSH keys on the machine, from reads that had been refused.
func TestBash_AFailureWithNoErrorMessageSaysTheCauseIsUnknown(t *testing.T) {
	ctx := toolapi.WithLevel(context.Background(), toolapi.ImpactObserve)
	got, _ := NewBash("").ExecuteTyped(ctx, map[string]any{"command": "cat /no/such/file 2>/dev/null"})
	for _, want := range []string{"printed no error message", "/dev/null", "cause is not known", "runs at level"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("the failure should say %q: %s", want, got.Detail)
		}
	}
}

// A program that is not installed: the shell's exit 127 says so even when its
// message was discarded.
func TestBash_ExitCode127SaysAProgramWasNotFound(t *testing.T) {
	ctx := toolapi.WithLevel(context.Background(), toolapi.ImpactObserve)
	got, _ := NewBash("").ExecuteTyped(ctx, map[string]any{"command": "no_such_program_xyz 2>/dev/null"})
	if !strings.Contains(got.Detail, "was not found (exit 127)") {
		t.Errorf("exit 127 should say a program was not found: %s", got.Detail)
	}
}
