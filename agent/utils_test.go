package agent

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
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
