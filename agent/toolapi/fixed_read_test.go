package toolapi

import (
	"context"
	"runtime"
	"testing"
)

// A fixed read runs as this program on Windows, where the lock reads nothing
// of WMI or the connections, and keeps the lock elsewhere.
func TestAFixedReadRunsAsThisProgramOnlyOnWindows(t *testing.T) {
	ctx := WithLevel(context.Background(), 100)
	got := IsEngine(ForFixedRead(ctx))
	if want := runtime.GOOS == "windows"; got != want {
		t.Errorf("on %s a fixed read runs as this program: %v, want %v", runtime.GOOS, got, want)
	}
	if l, _ := LevelOf(ForFixedRead(ctx)); l != 100 {
		t.Errorf("the level is kept: %d, want 100", l)
	}
}
