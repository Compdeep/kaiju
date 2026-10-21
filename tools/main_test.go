package tools

import (
	"context"
	"os"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
	"github.com/Compdeep/kaiju/sandbox"
)

// The tools start their programs through toolapi.Command, which re-starts this
// test binary to lock them, so the binary has to handle that first.
func TestMain(m *testing.M) {
	sandbox.Enter()
	sandbox.Enable(sandbox.Options{Logf: func(string, ...any) {}})
	os.Exit(m.Run())
}

// gitLevel runs git unlocked. git opens /dev/null to write whenever it starts,
// which seccomp alone (a kernel without Landlock) refuses, and these tests are
// about how the tool reports git's output, not about the lock.
func gitLevel() context.Context {
	return toolapi.WithLevel(context.Background(), toolapi.ImpactControl)
}
