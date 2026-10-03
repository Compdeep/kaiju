//go:build windows

package plugins

import (
	"fmt"
	"log"
	"os"
	"os/exec"
)

// ReExec starts the binary now on disk and asks this process to end.
//
// Windows has no execve, so the PID cannot be kept: the replacement is a new
// process and a supervisor watching this one will see it exit. That is a real
// difference from the Unix path, not a detail — a service manager configured to
// restart on exit will race this spawn. It is written this way because the
// alternative is no Windows support at all, and it has never been run on Windows.
//
// It returns only on failure.
func ReExec() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("couldn't find this binary's own path to restart: %w", err)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), convergedEnv+"=1")
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("couldn't start the new binary, so this process is still the old one: %w", err)
	}
	log.Printf("[plugins] started %s as pid %d; this process (%d) is exiting", exe, cmd.Process.Pid, os.Getpid())
	os.Exit(0)
	return nil // unreachable
}
