//go:build windows

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	Enter()
	Enable(Options{Logf: func(string, ...any) {}})
	os.Exit(m.Run())
}

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "in"), []byte("probe"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runCmd(t *testing.T, args ...string) string {
	t.Helper()
	out, _ := Command(context.Background(), 100, nil, "cmd", append([]string{"/C"}, args...)...).CombinedOutput()
	return string(out)
}

func TestALockedCommandCanRead(t *testing.T) {
	if r := Status(); r.SelfTest != nil {
		t.Fatalf("self-test: %v", r.SelfTest)
	}
	dir := fixture(t)
	if out := runCmd(t, "type", filepath.Join(dir, "in")); !strings.Contains(out, "probe") {
		t.Fatalf("could not read a file: %q", out)
	}
	if out := runCmd(t, "dir", dir); !strings.Contains(out, "in") {
		t.Fatalf("could not list a folder: %q", out)
	}
}

func TestALockedCommandCannotChangeAFile(t *testing.T) {
	cases := []struct {
		name    string
		args    func(dir string) []string
		creates string
	}{
		{"create", func(d string) []string { return []string{"echo", "x", ">", filepath.Join(d, "new")} }, "new"},
		{"append", func(d string) []string { return []string{"echo", "x", ">>", filepath.Join(d, "in")} }, ""},
		{"remove", func(d string) []string { return []string{"del", "/Q", filepath.Join(d, "in")} }, ""},
		{"rename", func(d string) []string { return []string{"ren", filepath.Join(d, "in"), "moved"} }, "moved"},
		{"copy", func(d string) []string { return []string{"copy", filepath.Join(d, "in"), filepath.Join(d, "copy")} }, "copy"},
		{"make a folder", func(d string) []string { return []string{"mkdir", filepath.Join(d, "sub")} }, "sub"},
	}
	for _, c := range cases {
		dir := fixture(t)
		runCmd(t, c.args(dir)...)
		if b, err := os.ReadFile(filepath.Join(dir, "in")); err != nil || string(b) != "probe" {
			t.Errorf("%s: the file was changed or removed: %q %v", c.name, b, err)
		}
		if c.creates != "" {
			if _, err := os.Stat(filepath.Join(dir, c.creates)); err == nil {
				t.Errorf("%s: %s was created", c.name, c.creates)
			}
		}
	}
}

func TestALockedCommandCannotWriteTheRegistry(t *testing.T) {
	runCmd(t, "reg", "add", `HKCU\Software\KaijuSandboxTest`, "/f")
	out := runCmd(t, "reg", "query", `HKCU\Software\KaijuSandboxTest`)
	if !strings.Contains(strings.ToLower(out), "unable to find") && strings.Contains(out, "KaijuSandboxTest") {
		t.Fatalf("a locked command wrote a registry key: %q", out)
	}
}

func TestAnUnlockedCommandCanWrite(t *testing.T) {
	dir := fixture(t)
	if err := Command(context.Background(), 200, nil, "cmd", "/C", "echo", "x", ">", filepath.Join(dir, "new")).Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new")); err != nil {
		t.Fatal("an unlocked command could not write")
	}
}

// A writable folder is labelled low integrity, so a locked command can write
// there and nowhere else.
func TestALockedCommandCanWriteAWritableFolder(t *testing.T) {
	work, other := t.TempDir(), fixture(t)
	cmd := Command(context.Background(), 100, []string{work}, "cmd", "/C", "echo", "x", ">", filepath.Join(work, "new"))
	if err := cmd.Run(); err != nil {
		t.Fatalf("could not write its writable folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "new")); err != nil {
		t.Fatal("no file in the writable folder")
	}
	runCmd(t, "echo", "x", ">", filepath.Join(other, "new"))
	if _, err := os.Stat(filepath.Join(other, "new")); err == nil {
		t.Fatal("wrote outside the writable folder")
	}
}
