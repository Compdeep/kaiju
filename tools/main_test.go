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

// controlStep is a step at control, for tests of what a tool does rather than
// of where its level lets it act: git, whose start opens /dev/null to write,
// which seccomp alone (a kernel without Landlock) refuses; archive and
// file_write, whose writes below control stay in the workspace.
func controlStep() context.Context {
	return toolapi.WithLevel(context.Background(), toolapi.ImpactControl)
}
