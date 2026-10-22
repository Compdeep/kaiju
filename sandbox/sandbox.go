// Package sandbox starts a tool's programs with only the effects the step's
// level allows (effects.go): by default reading, tracing, the network and
// signalling programs at every level, and changing files and root from 200.
//
// A tool cannot be trusted to say what a command does: bash rates a command
// by matching its text, and a script or a chmod it does not recognise is rated
// as a read. So the operating system holds the line instead, on the program
// itself, whatever the command is.
//
// Linux. The kernel lets a process lock itself, never unlock, and passes the
// lock to every process it starts. Go starts a program in one step, with no
// moment where code can run inside the new process, so a locked command is
// started as this program under the name Name; Enter, the first call in main,
// sees the name, locks the process and replaces it with the command (execve),
// which keeps the lock:
//
//   - the account, when ChangeFiles is refused and the application names one
//     (Options.Account): the command runs as that account, which owns
//     nothing, so file permissions refuse its changes to anyone else's files
//     and its signals to anyone else's programs. It writes where any user
//     may (/tmp, /dev/null), so temporary files, caches and git work, and no
//     seccomp rule on writes or signals is needed.
//   - capabilities: only those of the allowed effects are kept
//     (CAP_DAC_READ_SEARCH, CAP_SYS_PTRACE, CAP_SYSLOG, CAP_KILL), and only
//     when this program holds them.
//   - seccomp, always: mounts, modules, kexec, reboot, swap, io_uring (which
//     would make system calls seccomp does not see), open_by_handle_at,
//     pidfd_getfd, process_vm_writev; and for each refused effect the calls
//     that make it. With ReadPrograms allowed, ptrace is allowed request by
//     request: the requests that read, stop or resume a program, never those
//     that change it.
//   - Landlock (5.13 and later), when both ChangeFiles and ReadPrograms are
//     refused and there is no account: no file may be written, created, removed or renamed, except
//     under the writable folders, the sandbox's temporary folder and
//     /dev/null. Landlock refuses ptrace to any program outside the lock,
//     whatever the capabilities, so it cannot be used while ReadPrograms is
//     allowed; seccomp refuses the writes instead.
//   - seccomp, when ChangeFiles is refused with neither an account nor
//     Landlock: every system call
//     that writes a file. It cannot tell files apart, so /dev/null is passed
//     in already open instead (devNullFD) and a shell's redirections to it are
//     rewritten to use it.
//
// Windows. A command that may not change files is started with a copy of this
// program's rights at low integrity with its privileges removed except those
// that read (sandbox_windows.go). It can read; writes to files, the registry
// and other processes are refused, and everything it starts inherits the same
// rights. The writable folders are labelled low integrity, so it can write
// there. Network, ReadPrograms and SignalPrograms cannot be refused on Windows.
//
// Elsewhere. An application whose own program should not hold the rights a
// lock hands out (switching account, tracing) gives Options.Start: every
// command that needs the lock or root goes to a privileged program of its
// own, which decides the effects from its own policy and starts this program
// under Name already as the account and holding only the allowed
// capabilities; Enter then applies the rest. When that program cannot be
// reached, the command is locked here.
//
// A missing kernel feature does not stop a command: it runs with what this
// system can enforce, and Report says what cannot be. A lock that fails to
// apply does stop it, since an attacker could cause the failure on purpose,
// and a program that has not called Enable cannot start a locked command.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Name is the name this program is started under to lock a command. An
// application sets its own before calling Enter, so the locked process is
// recognisable in a process listing.
var Name = "kaiju-sandbox"

// Options configures Enable.
type Options struct {
	// Effects is asked for the table on every command, so a change to it
	// applies to the next one. DefaultEffects when nil.
	Effects func() Effects

	// Account names the account a command runs as when ChangeFiles is
	// refused (Linux). It should own nothing this program or the system
	// needs, and this program needs CAP_SETUID and CAP_SETGID to switch to
	// it. Empty, missing, or failing the self-test, commands run as this
	// program's own account under the lock that refuses every write.
	Account string

	// Start hands a command to another program that holds the rights and
	// applies the effects itself — an application's privileged service —
	// for every command this package would lock or run as root. level is the
	// step's; that program decides from its own policy, never from this one.
	// When it returns an error wrapping ErrUnavailable the command is locked
	// here instead, and refused if it needed Root; any other error refuses
	// it. Without Start, a command is locked here, and one allowed Root runs
	// as this program's own account, unlocked.
	Start func(ctx context.Context, cancel bool, level int, name string, args []string) (*exec.Cmd, error)

	// Logf receives what the sandbox reports: which lock is in use, the
	// self-test's result, and each refusal. log.Printf when nil.
	Logf func(format string, args ...any)
}

var (
	mu      sync.Mutex
	enabled bool
	opts    Options

	// What setup found, guarded by setupMu. Locked commands read it through
	// setup; the platform's self-test reads report and tmpDir while setup
	// holds the lock.
	setupMu   sync.Mutex
	setupDone bool
	setupAt   time.Time
	report    Report
	setupErr  error
	tmpDir    string
)

// retryAfter is how long a failed setup stands before the next locked command
// runs it again. A failure can pass — a full /tmp, a fork refused under load —
// and a setup that ran once and kept its failure refused every locked command
// until the program restarted. "No lock on this machine" is not retried: it
// does not change while the program runs.
var retryAfter = 30 * time.Second

// Report is what the sandbox found on this machine.
type Report struct {
	// Lock is the lock in use: "landlock+seccomp", "seccomp",
	// "restricted-token", or "" when there is none.
	Lock string
	// Account is the account a command that may not change files runs as,
	// or "" when it runs as this program's own.
	Account string
	// LandlockABI is the kernel's Landlock version, 0 when it has none.
	LandlockABI int
	// SelfTest is nil when a locked probe could read and could not write.
	SelfTest error
	// Unenforced are the effects this system cannot refuse: a command whose
	// level refuses one runs with it anyway. Every effect, when there is no
	// lock.
	Unenforced []Effect

	account *accountIDs
}

// accountIDs are Options.Account's ids and home folder.
type accountIDs struct {
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
	Home string `json:"-"`
}

// ErrUnavailable is wrapped by Options.Start when the program it hands
// commands to cannot be reached.
var ErrUnavailable = errors.New("the program that starts commands cannot be reached")

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
	enabled = true
	opts = o
	mu.Unlock()
	protectSelf()
}

// currentEffects is the application's table, or the default.
func currentEffects() Effects {
	mu.Lock()
	f := opts.Effects
	mu.Unlock()
	if f != nil {
		if e := f(); e != nil {
			return e
		}
	}
	return DefaultEffects()
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
	r, _, _ := setup()
	return r
}

/*
 * setup detects the lock, finds the account, makes the temporary folder when
 * there is no account, and runs the self-test, and returns what it found.
 * desc: A setup that passed is kept, and only its temporary folder is made
 *       again if something removed it (a clean-up of /tmp). One that found no
 *       lock is kept. Any other failure is kept for retryAfter, then run again.
 *       An account that fails the self-test is dropped, and the self-test run
 *       again without it, so a missing capability or account costs git and
 *       temporary files, never the lock.
 */
func setup() (Report, string, error) {
	setupMu.Lock()
	defer setupMu.Unlock()
	if setupDone {
		switch {
		case setupErr == nil:
			if report.account != nil {
				return report, "", nil
			}
			if _, err := os.Stat(tmpDir); err == nil {
				return report, tmpDir, nil
			}
			if dir, err := os.MkdirTemp("", "sandbox-tmp-"); err == nil {
				logf("[sandbox] the temporary folder %s was removed; using %s", tmpDir, dir)
				tmpDir = dir
				return report, tmpDir, nil
			}
		case errors.Is(setupErr, errNoLock), time.Since(setupAt) < retryAfter:
			return report, tmpDir, setupErr
		}
	}
	setupDone, setupAt = true, time.Now()
	report, setupErr = detect(), nil
	if report.Lock == "" {
		report.Unenforced = append([]Effect(nil), lockEffects...)
		setupErr = errNoLock
		logf("[sandbox] no lock is available on this system: commands run with every effect their account has")
		return report, tmpDir, setupErr
	}
	mu.Lock()
	name := opts.Account
	mu.Unlock()
	if name != "" {
		if a, err := accountFor(name); err != nil {
			logf("[sandbox] commands that may not change files run as this program's account, without temporary files: %v", err)
		} else if a != nil {
			report.Account, report.account = name, a
			report.SelfTest = selfTest()
			if report.SelfTest == nil {
				logf("[sandbox] lock in use: %s, commands that may not change files run as %s; self-test passed", report.Lock, name)
				return report, "", nil
			}
			logf("[sandbox] running commands as %s failed the self-test, so they run as this program's account: %v", name, report.SelfTest)
			report.Account, report.account = "", nil
		}
	}
	if tmpDir == "" {
		dir, err := os.MkdirTemp("", "sandbox-tmp-")
		if err != nil {
			setupErr = fmt.Errorf("create the sandbox's temporary folder: %w", err)
			logf("[sandbox] %v; tried again after %v", setupErr, retryAfter)
			return report, tmpDir, setupErr
		}
		tmpDir = dir
	}
	report.SelfTest = selfTest()
	if report.SelfTest != nil {
		setupErr = fmt.Errorf("self-test failed, so no locked command will run: %w", report.SelfTest)
		logf("[sandbox] %v; tried again after %v", setupErr, retryAfter)
		return report, tmpDir, setupErr
	}
	logf("[sandbox] lock in use: %s (landlock version %d); self-test passed", report.Lock, report.LandlockABI)
	return report, tmpDir, nil
}

/*
 * Command builds a command to start a program with the effects level allows.
 * desc: With Options.Start, a command that needs the lock or root is that
 *       program's. Otherwise, everything allowed but root, it is
 *       exec.CommandContext; root allowed, it runs unlocked; else it is
 *       started locked here. When the lock cannot be applied, or the command
 *       is refused, its Err is set, so Start returns the reason and nothing
 *       runs.
 * param: ctx - ends the command when done, as for exec.CommandContext.
 * param: level - the most the step that starts it may do.
 * param: writable - folders it may still write under when ChangeFiles is
 *        refused (where the lock can tell folders apart). A command run as
 *        Options.Account writes where that account may.
 * param: name - the program.
 * param: args - its arguments.
 * return: the command, used exactly as one from exec.CommandContext.
 */
func Command(ctx context.Context, level int, writable []string, name string, args ...string) *exec.Cmd {
	return start(ctx, true, level, writable, name, args)
}

/*
 * Detached is Command for a program that must outlive ctx, such as a server a
 * tool starts and leaves running. ctx is not watched.
 */
func Detached(level int, writable []string, name string, args ...string) *exec.Cmd {
	return start(context.Background(), false, level, writable, name, args)
}

func start(ctx context.Context, cancel bool, level int, writable []string, name string, args []string) *exec.Cmd {
	plain := func() *exec.Cmd {
		if cancel {
			return exec.CommandContext(ctx, name, args...)
		}
		return exec.Command(name, args...)
	}
	refuse := func(err error) *exec.Cmd {
		cmd := exec.Command(name, args...)
		cmd.Err = fmt.Errorf("sandbox: %s was not run: %w", name, err)
		logf("[sandbox] %v", cmd.Err)
		return cmd
	}

	mu.Lock()
	on, startElsewhere := enabled, opts.Start
	mu.Unlock()
	eff := currentEffects()
	root := eff.Allows(Root, level)
	allow := eff.allowedAt(level)
	refused := false
	for _, e := range lockEffects {
		refused = refused || !allow[e]
	}

	if startElsewhere != nil && (root || refused) {
		cmd, err := startElsewhere(ctx, cancel, level, name, args)
		switch {
		case err == nil:
			return cmd
		case root || !errors.Is(err, ErrUnavailable):
			return refuse(err)
		}
		logf("[sandbox] %v; %s is locked here instead", err, name)
	}
	if root || !refused {
		return plain()
	}
	if !on {
		return refuse(errors.New("this program does not call sandbox.Enter and sandbox.Enable"))
	}
	r, dir, setupErr := setup()
	if errors.Is(setupErr, errNoLock) {
		return plain()
	}
	if setupErr != nil {
		return refuse(setupErr)
	}
	if dir != "" {
		writable = append([]string{dir}, writable...)
	}
	cmd, err := platformCommand(ctx, cancel, r, allow, writable, name, args)
	if err != nil {
		return refuse(err)
	}
	return cmd
}
