package toolapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// stepAt is a step at level with ws as its workspace.
func stepAt(level int, ws string) context.Context {
	return WithWritable(WithLevel(context.Background(), level), ws)
}

// Below control a write lands in the workspace, and a symlink in it, to a
// folder or at the file's own name, does not send it elsewhere.
func TestAWriteBelowControlStaysInTheWorkspace(t *testing.T) {
	ws, away := t.TempDir(), t.TempDir()
	step := stepAt(ImpactAffect, ws)

	if err := WriteFileForStep(step, filepath.Join(ws, "a", "b.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("a write in the workspace: %v", err)
	}
	if err := os.Symlink(away, filepath.Join(ws, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(away, "target"), filepath.Join(ws, "link")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(ws, "out", "f"),        // through a symlinked folder
		filepath.Join(ws, "out", "new", "f"), // a folder made through one
		filepath.Join(ws, "link"),            // a symlink at the name itself
		filepath.Join(away, "f"),             // outside, named directly
	} {
		err := WriteFileForStep(step, p, []byte("x"), 0o644)
		if _, refused := Refused(err); !refused {
			t.Errorf("%s: %v, want a refusal", p, err)
		}
	}
	if err := MkdirAllForStep(step, filepath.Join(ws, "out", "d"), 0o755); err == nil {
		t.Error("a folder made through a symlink")
	}
	if entries, _ := os.ReadDir(away); len(entries) != 0 {
		t.Errorf("written outside the workspace: %v", entries)
	}
}

// At control, for the engine, a write goes anywhere; with no level or no
// workspace, nowhere.
func TestWhereAWriteMayLand(t *testing.T) {
	away := t.TempDir()
	for name, ctx := range map[string]context.Context{
		"control": stepAt(ImpactControl, t.TempDir()),
		"engine":  ForEngine(context.Background()),
	} {
		if err := WriteFileForStep(ctx, filepath.Join(away, name), []byte("x"), 0o644); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, ctx := range map[string]context.Context{
		"no level":     context.Background(),
		"nil":          nil,
		"no workspace": WithLevel(context.Background(), ImpactAffect),
	} {
		err := WriteFileForStep(ctx, filepath.Join(away, "f"), []byte("x"), 0o644)
		if _, refused := Refused(err); !refused {
			t.Errorf("%s: %v, want a refusal", name, err)
		}
	}
}

// A tool's own file stays inside its folder whatever the level, and a
// replaced file is whole.
func TestAToolsOwnFileStaysInItsFolder(t *testing.T) {
	ws, away := t.TempDir(), t.TempDir()
	if err := os.Symlink(away, filepath.Join(ws, "fetched")); err != nil {
		t.Fatal(err)
	}
	if _, refused := Refused(WriteFileIn(ws, filepath.Join(ws, "fetched", "page"), []byte("x"), 0o644)); !refused {
		t.Error("a saved page went through a symlink")
	}
	reg := filepath.Join(ws, "records.json")
	if err := ReplaceFileIn(ws, reg, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceFileIn(ws, reg, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(reg); string(b) != "two" {
		t.Errorf("replaced file holds %q", b)
	}
}
