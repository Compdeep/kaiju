//go:build !linux && !windows

package sandbox

import (
	"context"
	"os"
	"os/exec"
)

// No lock is implemented on this platform: a command runs with every effect its
// account has, and Report lists every effect as unenforced.

func Enter() {}

func checkNotChild() {}

func protectSelf() {}

func detect() Report { return Report{} }

func platformCommand(context.Context, bool, Report, map[Effect]bool, []string, string, []string) (*exec.Cmd, error) {
	return nil, errNoLock
}

func selfTest() error { return errNoLock }

func accountFor(string) (*accountIDs, error) { return nil, nil }

// ordinaryMayReadFile: no lock here, so nothing is withheld.
func ordinaryMayReadFile(*os.File) error { return nil }
