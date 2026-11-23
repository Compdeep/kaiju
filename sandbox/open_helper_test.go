package sandbox

import (
	"os"
	"testing"
)

// openForCheck opens path for a check made on the open file; the test fails when
// the file cannot be opened at all.
func openForCheck(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
