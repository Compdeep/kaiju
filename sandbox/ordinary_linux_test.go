//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// Level 0 reads what an account that owns nothing may: /etc/passwd yes,
// /etc/shadow and a private file no.
func TestTheOrdinaryCheckFollowsLinuxPermissions(t *testing.T) {
	if err := ordinaryMayReadFile(openForCheck(t, "/etc/passwd")); err != nil {
		t.Errorf("/etc/passwd refused: %v", err)
	}
	// Opened only when this test runs as root; otherwise it is not read anyway.
	if f, err := os.Open("/etc/shadow"); err == nil {
		defer f.Close()
		if ordinaryMayReadFile(f) == nil {
			t.Error("/etc/shadow allowed")
		}
	}
	dir := t.TempDir()
	own := filepath.Join(dir, "own")
	if err := os.WriteFile(own, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ordinaryMayReadFile(openForCheck(t, own)) == nil {
		t.Error("a 0600 file allowed")
	}
}
