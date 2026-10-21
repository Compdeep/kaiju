//go:build linux

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// childEnv is set on a locked command so that a program which reaches Enable
// without Enter having taken over — Enable called without Enter — stops instead
// of running itself again.
const childEnv = "KAIJU_SANDBOX_CHILD"

// devNullFD is the descriptor /dev/null is passed in on when seccomp alone holds
// the lock: seccomp cannot tell /dev/null from any other file, so opening it to
// write is refused, and a shell's ">/dev/null" is rewritten to ">&9".
const devNullFD = 9

// exitLockFailed is the exit status of a command whose lock could not be applied.
const exitLockFailed = 125

// forceSeccompOnly makes detect report no Landlock, so the seccomp-only lock can
// be tested on a kernel that has Landlock.
var forceSeccompOnly bool

// bootConfig is what the starting program tells the bootstrap, as its first
// argument: the command's environment may be replaced by the caller, the
// arguments are not.
type bootConfig struct {
	LandlockABI int      `json:"landlock_abi"`
	Writable    []string `json:"writable"`
	NullFD      bool     `json:"null_fd"`
}

/*
 * Enter is the bootstrap. Call it first in main.
 * desc: When this program was started under Name it locks the process and
 *       replaces it with the command named in its arguments, and never returns.
 *       Otherwise it returns at once.
 */
func Enter() {
	if len(os.Args) < 3 || filepath.Base(os.Args[0]) != Name {
		return
	}
	// The lock is applied to this thread, and execve must run on it.
	runtime.LockOSThread()
	os.Unsetenv(childEnv)

	var cfg bootConfig
	if err := json.Unmarshal([]byte(os.Args[1]), &cfg); err != nil {
		fail(exitLockFailed, fmt.Errorf("read the lock's settings: %w", err))
	}
	argv := os.Args[2:]
	path, err := exec.LookPath(argv[0])
	if err != nil {
		fail(127, err)
	}
	if err := lock(cfg); err != nil {
		fail(exitLockFailed, fmt.Errorf("could not lock the command, so it was not run: %w", err))
	}
	err = syscall.Exec(path, argv, os.Environ())
	fail(126, err)
}

func fail(code int, err error) {
	fmt.Fprintf(os.Stderr, "sandbox: %v\n", err)
	os.Exit(code)
}

// checkNotChild stops a program that was started to lock a command but did not
// call Enter, so it does not run itself again.
func checkNotChild() {
	if os.Getenv(childEnv) != "" {
		fail(exitLockFailed, errors.New("this program calls sandbox.Enable without calling sandbox.Enter first in main"))
	}
}

// lock applies every lock in cfg to this thread, which then starts the command.
func lock(cfg bootConfig) error {
	// The command leads its own process group, the only one it may signal. A
	// caller that made it a session leader (Setsid) already did this.
	self := os.Getpid()
	if pgid, _ := unix.Getpgid(0); pgid != self {
		if err := unix.Setpgid(0, 0); err != nil {
			return fmt.Errorf("own process group: %w", err)
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	if cfg.NullFD {
		fd, err := unix.Open("/dev/null", unix.O_WRONLY, 0)
		if err != nil {
			return fmt.Errorf("open /dev/null: %w", err)
		}
		if fd != devNullFD {
			if err := unix.Dup3(fd, devNullFD, 0); err != nil {
				return fmt.Errorf("pass /dev/null in: %w", err)
			}
			unix.Close(fd)
		}
	}
	if err := closeInherited(); err != nil {
		return fmt.Errorf("close inherited files: %w", err)
	}
	if cfg.LandlockABI > 0 {
		if err := applyLandlock(cfg.LandlockABI, cfg.Writable); err != nil {
			return fmt.Errorf("landlock: %w", err)
		}
	}
	if err := applySeccomp(self, cfg.LandlockABI == 0); err != nil {
		return fmt.Errorf("seccomp: %w", err)
	}
	return nil
}

/*
 * closeInherited marks every descriptor but input, output, error and devNullFD
 * close-on-exec, so the command gets none of them.
 * desc: The lock refuses opening a file to write; it does not stop a write
 *       through a descriptor already open for writing. Go opens its own files
 *       close-on-exec, but a descriptor whose flag was cleared, or one this
 *       program inherited, would reach the command.
 */
func closeInherited() error {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return err
	}
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil || fd <= 2 || fd == devNullFD {
			continue
		}
		// The descriptor ReadDir used is already closed, so EBADF is expected.
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil && err != unix.EBADF {
			return fmt.Errorf("descriptor %d: %w", fd, err)
		}
	}
	return nil
}

func detect() Report {
	if !seccompSupported() {
		return Report{}
	}
	if _, err := unix.PrctlRetInt(unix.PR_GET_SECCOMP, 0, 0, 0, 0); err != nil {
		return Report{}
	}
	abi := landlockABI()
	if forceSeccompOnly {
		abi = 0
	}
	if abi > 0 {
		return Report{Lock: "landlock+seccomp", LandlockABI: abi}
	}
	return Report{Lock: "seccomp"}
}

func landlockABI() int {
	v, _, e := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if e != 0 {
		return 0
	}
	return int(v)
}

/*
 * applyLandlock refuses every change to a file except under the given paths.
 * desc: Reading and running programs are not handled, so they stay allowed
 *       everywhere. A path that does not exist is skipped. /dev/null is always
 *       writable.
 */
func applyLandlock(abi int, writable []string) error {
	handled := uint64(unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	if abi >= 2 {
		handled |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		handled |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	const fileRights = unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE

	attr := unix.LandlockRulesetAttr{Access_fs: handled}
	rs, _, e := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if e != 0 {
		return fmt.Errorf("create ruleset: %w", e)
	}
	defer unix.Close(int(rs))

	for _, p := range append([]string{"/dev/null"}, writable...) {
		if p == "" {
			continue
		}
		fd, err := unix.Open(p, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			unix.Close(fd)
			return fmt.Errorf("stat %s: %w", p, err)
		}
		allowed := handled
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			allowed &= fileRights
		}
		rule := unix.LandlockPathBeneathAttr{Allowed_access: allowed, Parent_fd: int32(fd)}
		_, _, e := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, rs,
			unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&rule)), 0, 0, 0)
		unix.Close(fd)
		if e != 0 {
			return fmt.Errorf("allow %s: %w", p, e)
		}
	}
	if _, _, e := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, rs, 0, 0); e != 0 {
		return fmt.Errorf("restrict: %w", e)
	}
	return nil
}

// shells whose "-c" script has its /dev/null redirections rewritten.
var shells = map[string]bool{"sh": true, "bash": true, "dash": true, "ash": true, "zsh": true, "ksh": true}

// devNullRedirect matches an output redirection to /dev/null: ">", ">>", "N>",
// "N>>", "&>" and "&>>", with the character before it kept so a word ending in a
// digit or another redirection is not taken apart.
var devNullRedirect = regexp.MustCompile(`(^|[^<>&0-9])(&>>?|[0-9]*>>?)[ \t]*/dev/null\b`)

/*
 * rewriteDevNull points a shell script's output redirections to /dev/null at
 * the descriptor the bootstrap passes /dev/null in on.
 * return: the script, and whether it changed.
 */
func rewriteDevNull(script string) (string, bool) {
	out := devNullRedirect.ReplaceAllStringFunc(script, func(m string) string {
		sub := devNullRedirect.FindStringSubmatch(m)
		pre, op := sub[1], sub[2]
		if strings.HasPrefix(op, "&") {
			return fmt.Sprintf("%s>&%d 2>&%d", pre, devNullFD, devNullFD)
		}
		return fmt.Sprintf("%s%s>&%d", pre, strings.TrimRight(op, ">"), devNullFD)
	})
	return out, out != script
}

func platformCommand(ctx context.Context, cancel bool, r Report, writable []string, name string, args []string) (*exec.Cmd, error) {
	cfg := bootConfig{LandlockABI: r.LandlockABI, Writable: writable, NullFD: r.LandlockABI == 0}
	if cfg.NullFD && shells[filepath.Base(name)] && len(args) >= 2 && args[0] == "-c" {
		if s, changed := rewriteDevNull(args[1]); changed {
			logf("[sandbox] seccomp only, so /dev/null cannot be opened to write; ran %q as %q", args[1], s)
			args = append([]string{args[0], s}, args[2:]...)
		}
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	const self = "/proc/self/exe"
	var cmd *exec.Cmd
	if cancel {
		cmd = exec.CommandContext(ctx, self)
	} else {
		cmd = exec.Command(self)
	}
	cmd.Args = append([]string{Name, string(b), name}, args...)
	cmd.Env = append(os.Environ(), childEnv+"=1", "TMPDIR="+writable[0])
	return cmd, nil
}

// probeCommand starts the self-test's command; a test replaces it with one that
// does not lock, to show the self-test catches a lock that does not hold.
var probeCommand = platformCommand

/*
 * selfTest runs one locked command that reads a file and tries to change
 * others, and fails unless the read worked and every change was refused.
 */
func selfTest() error {
	dir, err := os.MkdirTemp("", "sandbox-selftest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	in, out, keep := filepath.Join(dir, "in"), filepath.Join(dir, "out"), filepath.Join(dir, "keep")
	if err := os.WriteFile(in, []byte("probe"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		return err
	}
	cmd, err := probeCommand(context.Background(), true, report, []string{tmpDir}, "sh",
		[]string{"-c", `cat "$1"; echo x > "$2"; chmod 777 "$3"; rm -f "$3"`, "sh", in, out, keep})
	if err != nil {
		return err
	}
	got, _ := cmd.Output()
	if string(got) != "probe" {
		return fmt.Errorf("a locked command could not read a file: got %q", got)
	}
	if _, err := os.Stat(out); err == nil {
		return errors.New("a locked command created a file")
	}
	st, err := os.Stat(keep)
	if err != nil {
		return errors.New("a locked command removed a file")
	}
	if st.Mode().Perm() != 0o600 {
		return errors.New("a locked command changed a file's permissions")
	}
	return nil
}
