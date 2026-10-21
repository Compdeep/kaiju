//go:build !linux && !windows

package sandbox

import (
	"context"
	"os/exec"
)

// No lock is implemented on this platform: a locked command is refused, or runs
// unlocked under Options.AllowUnlocked.

func Enter() {}

func checkNotChild() {}

func detect() Report { return Report{} }

func platformCommand(context.Context, bool, Report, []string, string, []string) (*exec.Cmd, error) {
	return nil, errNoLock
}

func selfTest() error { return errNoLock }
