package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Below control archive writes only into its step's workspace: no lock holds
// its own code, and extraction is a way to put files anywhere.
func TestArchiveWritesOnlyIntoTheWorkspaceBelowControl(t *testing.T) {
	work, elsewhere := t.TempDir(), t.TempDir()
	step := toolapi.WithWritable(toolapi.WithLevel(context.Background(), toolapi.ImpactAffect), work)

	if err := mayWriteAt(step, filepath.Join(work, "out")); err != nil {
		t.Errorf("the workspace refused: %v", err)
	}
	if err := mayWriteAt(step, filepath.Join(elsewhere, "out")); err == nil {
		t.Error("a folder outside the workspace allowed at 100")
	}
	msg, err := NewArchive().ExecuteTyped(step, map[string]any{
		"action": "extract", "archive_path": filepath.Join(work, "x.zip"), "dest": elsewhere,
	})
	if err != nil || msg.Status != toolapi.StatusError {
		t.Errorf("extract outside the workspace at 100: %v %s", err, msg.Status)
	}
	control := toolapi.WithLevel(context.Background(), toolapi.ImpactControl)
	if err := mayWriteAt(control, filepath.Join(elsewhere, "out")); err != nil {
		t.Errorf("at control: %v", err)
	}
}
