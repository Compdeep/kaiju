//go:build windows

package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A path is data to disk_usage: a quote in it no longer closes the script's
// string and runs what follows. It runs as this program (a fixed read), so a
// script that escaped would be able to write.
func TestADiskUsagePathIsNotScript(t *testing.T) {
	dir := t.TempDir()
	mark := filepath.Join(dir, "escaped")
	path := dir + `' ; New-Item -ItemType File -Path '` + mark + `' ; '`
	ctx := toolapi.WithLevel(context.Background(), toolapi.ImpactObserve)
	_, _ = NewDiskUsage().Execute(ctx, map[string]any{"path": path})
	if _, err := os.Stat(mark); err == nil {
		t.Fatalf("the path ran as PowerShell: %s was made", mark)
	}
}

// A quiet process is found by its name as Windows' tools write it, with
// ".exe": winlogon is never among the busiest, and "winlogon.exe" matched no
// Get-Process row. At level 0, where the lock used to hide it.
func TestProcessListFindsAQuietProcessByItsExeName(t *testing.T) {
	ctx := toolapi.WithLevel(context.Background(), toolapi.ImpactObserve)
	out, err := NewProcessList().Execute(ctx, map[string]any{"filter": "winlogon.exe"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(out), "winlogon") {
		t.Errorf("winlogon not listed:\n%s", out)
	}
}
