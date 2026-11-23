//go:build windows

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Level 0 reads what an ordinary user may, by Windows' own access check: a
// file every user may read is readable, one only its owner, SYSTEM and
// Administrators may read (every user profile, this test's temp folder) is not.
func TestTheOrdinaryCheckFollowsWindowsPermissions(t *testing.T) {
	hosts := filepath.Join(os.Getenv("SystemRoot"), "System32", "drivers", "etc", "hosts")
	if err := ordinaryMayReadFile(openForCheck(t, hosts)); err != nil {
		t.Errorf("hosts is readable by every user, refused: %v", err)
	}
	own := filepath.Join(t.TempDir(), "own")
	if err := os.WriteFile(own, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ordinaryMayReadFile(openForCheck(t, own)); err == nil {
		t.Error("a file only its owner may read was allowed")
	}
}

// A command on the level-0 token sees this process, reads what every user may,
// cannot read a private file and cannot write.
func TestALevelZeroCommandReadsAsAnOrdinaryUser(t *testing.T) {
	allow := map[Effect]bool{ReadFiles: false, ReadPrograms: false, ReadKernelLog: false, Network: true, ChangeFiles: false, SignalPrograms: false}
	run := func(name string, args ...string) string {
		cmd, err := platformCommand(context.Background(), true, report, allow, nil, name, args)
		if err != nil {
			t.Fatalf("not started: %v", err)
		}
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	pid := strconv.Itoa(os.Getpid())
	if got := strings.TrimSpace(run("powershell", "-NoProfile", "-NonInteractive", "-Command", "(Get-Process -Id "+pid+").Id")); got != pid {
		t.Errorf("does not see this process %s: %q", pid, got)
	}
	hosts := filepath.Join(os.Getenv("SystemRoot"), "System32", "drivers", "etc", "hosts")
	if out := run("cmd", "/C", "type", hosts); strings.TrimSpace(out) == "" || strings.Contains(strings.ToLower(out), "denied") {
		t.Errorf("could not read hosts: %q", out)
	}
	dir := t.TempDir()
	private := filepath.Join(dir, "private")
	if err := os.WriteFile(private, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := run("cmd", "/C", "type", private); strings.Contains(out, "secret") {
		t.Error("read a file only its owner may read")
	}
	run("cmd", "/C", "echo", "x", ">", filepath.Join(dir, "new"))
	if _, err := os.Stat(filepath.Join(dir, "new")); err == nil {
		t.Error("created a file")
	}
}

// MayReadFile: below the level that reads every file, only what an ordinary user
// may; at it, everything. Under Enbarr's table (reading every file from 100).
func TestMayReadFollowsTheLevel(t *testing.T) {
	e := DefaultEffects()
	e[ReadFiles] = 100
	Enable(Options{Effects: func() Effects { return e }, Logf: func(string, ...any) {}})
	t.Cleanup(func() { Enable(Options{Logf: func(string, ...any) {}}) })

	own := filepath.Join(t.TempDir(), "own")
	if err := os.WriteFile(own, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := MayReadFile(0, openForCheck(t, own))
	if err == nil || !strings.Contains(err.Error(), "needs level 100") {
		t.Errorf("a private file at 0: %v, want refused naming level 100", err)
	}
	if err := MayReadFile(100, openForCheck(t, own)); err != nil {
		t.Errorf("a private file at 100: %v", err)
	}
	hosts := filepath.Join(os.Getenv("SystemRoot"), "System32", "drivers", "etc", "hosts")
	if err := MayReadFile(0, openForCheck(t, hosts)); err != nil {
		t.Errorf("hosts at 0: %v", err)
	}
}
