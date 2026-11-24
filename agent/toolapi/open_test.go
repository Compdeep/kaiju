package toolapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A path that reaches the network or a device is refused on Windows before
// anything is opened; a local path is not.
func TestLocalPathRefusesSharesAndDevices(t *testing.T) {
	for _, p := range []string{`\\attacker\share\x`, `//attacker/share/x`, `\\?\C:\Windows\win.ini`, `\\.\PhysicalDrive0`, `\\?\UNC\attacker\share\x`} {
		err := LocalPath(p)
		if runtime.GOOS == "windows" {
			var rr *Refusal
			if !errors.As(err, &rr) {
				t.Errorf("%s: %v, want a refusal", p, err)
			}
		} else if err != nil {
			t.Errorf("%s refused off Windows: %v", p, err)
		}
	}
	if err := LocalPath(filepath.Join(os.TempDir(), "x")); err != nil {
		t.Errorf("a local path refused: %v", err)
	}
}

// OpenForStep opens a file the engine reads, and a share is refused for a
// step as for the engine.
func TestOpenForStep(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fh, err := OpenForStep(context.Background(), f)
	if err != nil {
		t.Fatalf("the engine's read refused: %v", err)
	}
	fh.Close()
	if runtime.GOOS == "windows" {
		var rr *Refusal
		if _, err := OpenForStep(WithLevel(context.Background(), 200), `\\attacker\share\x`); !errors.As(err, &rr) {
			t.Errorf("a share at 200: %v, want a refusal", err)
		}
	}
	if _, err := OpenForStep(WithLevel(context.Background(), 200), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing file opened")
	} else if rr := (*Refusal)(nil); errors.As(err, &rr) {
		t.Errorf("a missing file is the open's error, not a refusal: %v", err)
	}
}
