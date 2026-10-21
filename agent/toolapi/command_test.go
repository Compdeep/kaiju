package toolapi

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Compdeep/kaiju/sandbox"
)

func TestMain(m *testing.M) {
	sandbox.Enter()
	sandbox.Enable(sandbox.Options{Logf: func(string, ...any) {}})
	os.Exit(m.Run())
}

// writes reports whether a shell started through Command under ctx could
// create a file.
func writes(t *testing.T, ctx context.Context) bool {
	t.Helper()
	dir := t.TempDir()
	Command(ctx, "sh", "-c", `echo x > "$1/f"`, "sh", dir).Run()
	_, err := os.Stat(filepath.Join(dir, "f"))
	return err == nil
}

// The level decides, not the tool: below control a program is locked, at
// control it is not, and a context with no level is locked.
func TestAProgramIsLockedBelowControl(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the shell commands here are Linux's")
	}
	cases := []struct {
		name  string
		ctx   context.Context
		write bool
	}{
		{"no level", context.Background(), false},
		{"observe", WithLevel(context.Background(), ImpactObserve), false},
		{"affect", WithLevel(context.Background(), ImpactAffect), false},
		{"between affect and control", WithLevel(context.Background(), 150), false},
		{"control", WithLevel(context.Background(), ImpactControl), true},
	}
	for _, c := range cases {
		if got := writes(t, c.ctx); got != c.write {
			t.Errorf("%s: wrote=%v, want %v", c.name, got, c.write)
		}
		if Locked(c.ctx) == c.write {
			t.Errorf("%s: Locked=%v", c.name, Locked(c.ctx))
		}
	}
}

// A folder named writable stays writable to a locked program where the kernel
// can tell folders apart (Landlock); with seccomp alone nothing is writable.
func TestAWritableFolderStaysWritable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the shell commands here are Linux's")
	}
	dir := t.TempDir()
	ctx := WithWritable(WithLevel(context.Background(), ImpactAffect), dir)
	Command(ctx, "sh", "-c", `echo x > "$1/f"`, "sh", dir).Run()
	_, err := os.Stat(filepath.Join(dir, "f"))
	landlock := sandbox.Status().LandlockABI > 0
	if landlock && err != nil {
		t.Fatal("a locked program could not write in a folder named writable")
	}
	if !landlock && err == nil {
		t.Fatal("seccomp alone let a file be written")
	}
}
