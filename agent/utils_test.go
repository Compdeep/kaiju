package agent

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Several runs start at once on a busy node, and each rotates the service logs
// so it does not read the previous run's output. Two doing it together rename
// the same files twice: the second set of renames either fails, or moves a file
// the first one just moved, and a run then reads log output belonging to another
// run — worse than reading none.
//
// The second run skips rather than waits. That is not an approximation: the
// point is that the files are fresh, and the first run is already making them so.
func TestOnlyOneRunRotatesTheServiceLogsAtATime(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, ".services")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web.log", "db.log", "api.err.log"} {
		if err := os.WriteFile(filepath.Join(logs, name), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rotateServiceLogs(dir)
		}()
	}
	wg.Wait()

	// Whoever won, every log is now a .prev and nothing was renamed twice into
	// a .prev.prev.
	entries, err := os.ReadDir(logs)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".prev.prev") {
			t.Errorf("%s was rotated twice", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(logs, "web.log.prev")); err != nil {
		t.Errorf("web.log was not rotated: %v", err)
	}
}

// The flag is released, so the next run rotates rather than skipping for ever.
func TestRotationIsAvailableAgainAfterwards(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, ".services")
	_ = os.MkdirAll(logs, 0o755)

	_ = os.WriteFile(filepath.Join(logs, "one.log"), []byte("x"), 0o644)
	rotateServiceLogs(dir)
	_ = os.WriteFile(filepath.Join(logs, "two.log"), []byte("x"), 0o644)
	rotateServiceLogs(dir)

	if _, err := os.Stat(filepath.Join(logs, "two.log.prev")); err != nil {
		t.Errorf("the second rotation did not happen: %v", err)
	}
}

// A run that arrives while another is rotating waits for it.
//
// It used to return immediately, and then read the directory while the first
// run was still part-way through moving it — a log the first had not reached
// yet is the previous run's output, in the run rotation exists to give fresh
// output to.
//
// Held exactly rather than by timing: the test takes the lock itself, so the
// call under test cannot proceed until the test lets it.
func TestASecondRotationWaitsForTheFirst(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, ".services")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "web.log"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	rotating.Lock() // stand in for a run that is part-way through

	done := make(chan struct{})
	go func() {
		rotateServiceLogs(dir)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("the second rotation returned while another was still holding the directory")
	case <-time.After(50 * time.Millisecond):
		// Still waiting, which is the point.
	}

	rotating.Unlock()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the second rotation never returned after the first released")
	}

	if _, err := os.Stat(filepath.Join(logs, "web.log.prev")); err != nil {
		t.Errorf("it waited and then did not rotate: %v", err)
	}
}

// The agent's own session state is not part of the workspace it describes.
//
// sessions/ holds one directory per conversation the deployment has had — 283
// on the install this was found on — and the scan stops at 120 entries. Reached
// breadth-first they arrived before anything a person had put there, so a
// planner's entire view of the workspace was a list of conversation ids.
//
// Skipped at the root only: "sessions" is a name a project may use for its own
// directory, and skipDirs matches at every depth.
func TestWorkspaceTreeLeavesOutTheAgentsOwnSessions(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{
		"sessions/aaaa-1111/ctx", "sessions/bbbb-2222/ctx", "sessions/cccc-3333/ctx",
		"project/myapp/src", "project/myapp/sessions", "media",
	} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "project/myapp/src/main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tree := scanWorkspaceTree(root, 5)

	if strings.Contains(tree, "sessions/aaaa-1111") {
		t.Errorf("the agent's session state is in the workspace listing:\n%s", tree)
	}
	if !strings.Contains(tree, "project/myapp/sessions") {
		t.Errorf("a project's OWN sessions directory was skipped too — the rule reached past the root:\n%s", tree)
	}
	for _, want := range []string{"project/myapp/src", "media"} {
		if !strings.Contains(tree, want) {
			t.Errorf("%q is missing from the listing:\n%s", want, tree)
		}
	}
}
