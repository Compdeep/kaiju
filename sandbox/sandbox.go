// Package sandbox starts a tool's programs so that they can read and cannot
// change anything.
//
// A step allowed less than the most a run may do should not change the machine
// it runs on, and a tool cannot be trusted to say whether a command changes
// anything: bash rates a command by matching its text, and a script or a chmod
// it does not recognise is rated as a read. So the operating system holds the
// line instead, on the program itself, whatever the command is.
//
// Linux. The kernel lets a process lock itself, never unlock, and passes the
// lock to every process it starts. Go starts a program in one step, with no
// moment where code can run inside the new process, so a locked command is
// started as this program under the name Name; Enter, the first call in main,
// sees the name, locks the process and replaces it with the command (execve),
// which keeps the lock:
//
//   - Landlock (5.13 and later): no file may be written, created, removed or
//     renamed, except under the writable folders, the sandbox's temporary
//     folder and /dev/null.
//   - seccomp, always: no signal outside the command's own process group (so
//     a shell cannot stop its own child by process id, and timeout(1) cannot
//     stop its command; the tool's own time limit stops it instead), no
//     ptrace, no reading or writing another process's memory, no
//     change to permissions, owners, times or extended attributes, no
//     truncate, no io_uring (which would open files without a system call
//     seccomp sees), no mounts or modules. Landlock does not cover these.
//   - seccomp, when the kernel has no Landlock: also every system call that
//     writes a file. It cannot tell files apart, so /dev/null is passed in
//     already open instead (devNullFD) and a shell's redirections to it are
//     rewritten to use it.
//
// Windows. The command is started with a copy of this program's rights at low
// integrity with its privileges removed except those that read
// (sandbox_windows.go). It can read; writes to files, the registry and other
// processes are refused, and everything it starts inherits the same rights.
//
// When no lock can be applied, a locked command is not run. A program that has
// not called Enable cannot start one either, and neither can one whose
// self-test found the lock does not hold.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
)

// Name is the name this program is started under to lock a command. An
// application sets its own before calling Enter, so the locked process is
// recognisable in a process listing.
var Name = "kaiju-sandbox"

// Options configures Enable.
type Options struct {
	// AllowUnlocked runs a locked command without a lock on a platform that
	// has none, logging that it did. Off, such a command is refused. For a
	// developer's own machine only.
	AllowUnlocked bool

	// Logf receives what the sandbox reports: which lock is in use, the
	// self-test's result, and each refusal. log.Printf when nil.
	Logf func(format string, args ...any)
}

var (
	mu      sync.Mutex
	enabled bool
	opts    Options

	setupOnce sync.Once
	report    Report
	setupErr  error
	tmpDir    string
)

// Report is what the sandbox found on this machine.
type Report struct {
	// Lock is the lock in use: "landlock+seccomp", "seccomp",
	// "restricted-token", or "" when there is none.
	Lock string
	// LandlockABI is the kernel's Landlock version, 0 when it has none.
	LandlockABI int
	// SelfTest is nil when a locked probe could read and could not write.
	SelfTest error
}

// errNoLock is returned for a locked command on a platform without a lock.
var errNoLock = errors.New("this platform has no way to lock a command")

/*
 * Enable says this program can start a locked command: it calls Enter first in
 * main. Without it every locked command is refused, since starting this program
 * under Name would run the program again rather than the command.
 * param: o - options; the zero value refuses where no lock is available.
 */
func Enable(o Options) {
	checkNotChild()
	mu.Lock()
	defer mu.Unlock()
	enabled = true
	opts = o
}

func logf(format string, args ...any) {
	mu.Lock()
	f := opts.Logf
	mu.Unlock()
	if f == nil {
		f = log.Printf
	}
	f(format, args...)
}

/*
 * Status reports the lock in use and the self-test's result, running the
 * self-test if no locked command has run yet.
 * return: the report.
 */
func Status() Report {
	setup()
	return report
}

// setup detects the lock and runs the self-test, once.
func setup() {
	setupOnce.Do(func() {
		report = detect()
		if report.Lock == "" {
			setupErr = errNoLock
			logf("[sandbox] no lock is available on this machine")
			return
		}
		dir, err := os.MkdirTemp("", "sandbox-tmp-")
		if err != nil {
			setupErr = fmt.Errorf("create the sandbox's temporary folder: %w", err)
			logf("[sandbox] %v", setupErr)
			return
		}
		tmpDir = dir
		report.SelfTest = selfTest()
		if report.SelfTest != nil {
			setupErr = fmt.Errorf("self-test failed, so no locked command will run: %w", report.SelfTest)
			logf("[sandbox] %v", setupErr)
			return
		}
		logf("[sandbox] lock in use: %s (landlock version %d); self-test passed", report.Lock, report.LandlockABI)
	})
}

/*
 * Command builds a command to start a program, locked or not.
 * desc: Unlocked, it is exec.CommandContext. Locked, it is started so that it
 *       can read and cannot change anything; when that cannot be done the
 *       command's Err is set, so Start returns the reason and nothing runs.
 * param: ctx - ends the command when done, as for exec.CommandContext.
 * param: locked - whether to lock it.
 * param: writable - folders a locked command may still write under.
 * param: name - the program.
 * param: args - its arguments.
 * return: the command, used exactly as one from exec.CommandContext.
 */
func Command(ctx context.Context, locked bool, writable []string, name string, args ...string) *exec.Cmd {
	if !locked {
		return exec.CommandContext(ctx, name, args...)
	}
	return lockedCommand(ctx, true, writable, name, args)
}

/*
 * Detached is Command for a program that must outlive ctx, such as a server a
 * tool starts and leaves running. ctx is not watched.
 */
func Detached(locked bool, writable []string, name string, args ...string) *exec.Cmd {
	if !locked {
		return exec.Command(name, args...)
	}
	return lockedCommand(context.Background(), false, writable, name, args)
}

func lockedCommand(ctx context.Context, cancel bool, writable []string, name string, args []string) *exec.Cmd {
	mu.Lock()
	on, allowUnlocked := enabled, opts.AllowUnlocked
	mu.Unlock()

	refuse := func(err error) *exec.Cmd {
		cmd := exec.Command(name, args...)
		cmd.Err = fmt.Errorf("sandbox: %s was not run: %w", name, err)
		logf("[sandbox] %v", cmd.Err)
		return cmd
	}
	if !on {
		return refuse(errors.New("this program does not call sandbox.Enter and sandbox.Enable"))
	}
	setup()
	if errors.Is(setupErr, errNoLock) && allowUnlocked {
		logf("[sandbox] %s runs without a lock: none is available and AllowUnlocked is set", name)
		if cancel {
			return exec.CommandContext(ctx, name, args...)
		}
		return exec.Command(name, args...)
	}
	if setupErr != nil {
		return refuse(setupErr)
	}
	cmd, err := platformCommand(ctx, cancel, report, append([]string{tmpDir}, writable...), name, args)
	if err != nil {
		return refuse(err)
	}
	return cmd
}
