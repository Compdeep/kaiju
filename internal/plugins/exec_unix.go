//go:build !windows

package plugins

import (
	"fmt"
	"log"
	"os"
	"syscall"
)

// ReExec replaces this process with the binary now on disk, in place.
//
// syscall.Exec keeps the PID. A supervisor — PM2 here, systemd elsewhere — sees
// no exit, counts no restart, and runs none of its backoff. That is the whole
// reason for exec over spawn-and-die: the process that comes back IS the process
// the supervisor is watching.
//
// It returns only on failure. On success nothing after it runs.
func ReExec() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("couldn't find this binary's own path to re-exec: %w", err)
	}
	// Marks the replacement as already converged once, so a rebuild that did not
	// satisfy the request cannot exec in a loop.
	env := append(os.Environ(), convergedEnv+"=1")
	log.Printf("[plugins] re-exec %s (pid %d is kept)", exe, os.Getpid())
	if err := syscall.Exec(exe, os.Args, env); err != nil {
		return fmt.Errorf("re-exec failed, so this process is still the old binary: %w", err)
	}
	return nil // unreachable
}
