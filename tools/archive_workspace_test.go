package tools

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Below control archive writes only into its step's workspace, a symlink in it
// included: no lock holds its own code, and extraction is a way to put files
// anywhere. At control it extracts anywhere.
func TestArchiveWritesOnlyIntoTheWorkspaceBelowControl(t *testing.T) {
	work, elsewhere := t.TempDir(), t.TempDir()
	zipPath := filepath.Join(work, "x.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("f.txt")
	w.Write([]byte("x"))
	zw.Close()
	f.Close()
	if err := os.Symlink(elsewhere, filepath.Join(work, "link")); err != nil {
		t.Fatal(err)
	}

	step := toolapi.WithWritable(toolapi.WithLevel(context.Background(), toolapi.ImpactAffect), work)
	extract := func(ctx context.Context, dest string) toolapi.ToolMessage {
		msg, err := NewArchive().ExecuteTyped(ctx, map[string]any{"action": "extract", "archive_path": zipPath, "dest": dest})
		if err != nil {
			t.Fatalf("extract into %s: %v", dest, err)
		}
		return msg
	}
	if msg := extract(step, filepath.Join(work, "out")); msg.Status != toolapi.StatusOK {
		t.Errorf("into the workspace: %s", msg.Status)
	}
	for _, dest := range []string{elsewhere, filepath.Join(work, "link")} {
		if msg := extract(step, dest); msg.Status != toolapi.StatusError {
			t.Errorf("into %s at 100: %s, want refused", dest, msg.Status)
		}
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("extracted outside the workspace at 100: %v", entries)
	}
	if msg := extract(toolapi.WithLevel(context.Background(), toolapi.ImpactControl), elsewhere); msg.Status != toolapi.StatusOK {
		t.Errorf("at control: %s", msg.Status)
	}
}
