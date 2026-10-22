package toolapi

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Compdeep/kaiju/sandbox"
)

// As Kaiju's command line: tracing given up below root, so Landlock can keep a
// writable folder writable where the kernel has it.
func TestMain(m *testing.M) {
	sandbox.Enter()
	sandbox.Enable(sandbox.Options{Logf: func(string, ...any) {}, Effects: func() sandbox.Effects {
		e := sandbox.DefaultEffects()
		e[sandbox.ReadPrograms] = sandbox.Never
		return e
	}})
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

// The level decides, not the tool: below control a program may not change
// files, at control it may, a context with no level is level 0, and the
// engine's own programs are not held.
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
		{"the engine's own", ForEngine(context.Background()), true},
	}
	for _, c := range cases {
		if got := writes(t, c.ctx); got != c.write {
			t.Errorf("%s: wrote=%v, want %v", c.name, got, c.write)
		}
		if (Limits(c.ctx) == "") != c.write {
			t.Errorf("%s: Limits=%q", c.name, Limits(c.ctx))
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
