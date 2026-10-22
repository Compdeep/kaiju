//go:build linux

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
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

// bootConfig is what the starting program tells the bootstrap, as its first
// argument: the command's environment may be replaced by the caller, the
// arguments are not.
type bootConfig struct {
	Allow       map[Effect]bool `json:"allow"`
	Account     *accountIDs     `json:"account,omitempty"`
	LandlockABI int             `json:"landlock_abi"`
	Writable    []string        `json:"writable"`
	NullFD      bool            `json:"null_fd"`
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
	if cfg.Account != nil {
		if err := switchAccount(*cfg.Account); err != nil {
			return fmt.Errorf("account: %w", err)
		}
	}
	if err := dropCapabilities(capabilitiesFor(cfg.Allow)); err != nil {
		return fmt.Errorf("capabilities: %w", err)
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
	if err := applySeccomp(self, cfg.Allow, cfg.NullFD, cfg.Account != nil); err != nil {
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

// effectCapabilities are the capabilities a locked command may keep, each for
// the effect it serves: CAP_DAC_READ_SEARCH reads and lists any file;
// CAP_SYS_PTRACE traces and reads any program, and the filter refuses the
// ptrace requests that change one; CAP_SYSLOG reads the kernel log, and the
// filter allows only syslog's read actions; CAP_KILL signals any program. Any
// other the starting program holds (CAP_SETUID and CAP_SETGID, to switch to
// the account; CAP_NET_BIND_SERVICE, to listen below 1024; all of them when
// it runs as root) is dropped.
var effectCapabilities = map[Effect]uint{
	ReadFiles:      unix.CAP_DAC_READ_SEARCH,
	ReadPrograms:   unix.CAP_SYS_PTRACE,
	ReadKernelLog:  unix.CAP_SYSLOG,
	SignalPrograms: unix.CAP_KILL,
}

// capabilitiesFor are the capabilities of the allowed effects.
func capabilitiesFor(allow map[Effect]bool) []uint {
	var keep []uint
	for _, e := range lockEffects {
		if c, ok := effectCapabilities[e]; ok && allow[e] {
			keep = append(keep, c)
		}
	}
	return keep
}

// Securebits: root does not regain its capabilities when it starts a program,
// and that cannot be undone.
const (
	secbitNoRoot       = 1 << 0
	secbitNoRootLocked = 1 << 1
)

/*
 * dropCapabilities leaves this process, and every program it starts, with
 * only the capabilities in keep, and only those it already held.
 * desc: A capability passes to a started program when it is ambient, so each
 *       other ambient capability is lowered, and permitted, effective and
 *       inheritable are cut to the kept ones, which are then made ambient. As
 *       root, a started program would get every capability back for being
 *       root; SECBIT_NOROOT stops that.
 */
func dropCapabilities(keep []uint) error {
	if os.Geteuid() == 0 {
		if err := unix.Prctl(unix.PR_SET_SECUREBITS, secbitNoRoot|secbitNoRootLocked, 0, 0, 0); err != nil {
			return fmt.Errorf("securebits: %w", err)
		}
	}
	var mask [2]uint32
	isKept := map[uint]bool{}
	for _, c := range keep {
		mask[c/32] |= 1 << (c % 32)
		isKept[c] = true
	}
	for c := uint(0); c <= unix.CAP_LAST_CAP; c++ {
		if !isKept[c] {
			// EINVAL for a capability this kernel does not know: nothing to lower.
			_ = unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_LOWER, uintptr(c), 0, 0)
		}
	}
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		return fmt.Errorf("read: %w", err)
	}
	for i := range data {
		data[i].Permitted &= mask[i]
		data[i].Effective &= mask[i]
		data[i].Inheritable = data[i].Permitted
	}
	if err := unix.Capset(&hdr, &data[0]); err != nil {
		return fmt.Errorf("set: %w", err)
	}
	for _, c := range keep {
		if data[c/32].Permitted&(1<<(c%32)) != 0 {
			if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_RAISE, uintptr(c), 0, 0); err != nil {
				return fmt.Errorf("keep capability %d: %w", c, err)
			}
		}
	}
	return nil
}

/*
 * accountFor finds the account a command that may not change files runs as.
 * return: its ids, or why it cannot be used: it does not exist, it is root or
 *         this program's own, or this program cannot switch to it.
 */
func accountFor(name string) (*accountIDs, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return nil, err
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil {
		return nil, fmt.Errorf("account %s has ids %q and %q", name, u.Uid, u.Gid)
	}
	if uid == 0 || uid == os.Getuid() {
		return nil, fmt.Errorf("account %s is root or this program's own", name)
	}
	if os.Geteuid() != 0 {
		hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
		var data [2]unix.CapUserData
		if err := unix.Capget(&hdr, &data[0]); err != nil {
			return nil, fmt.Errorf("read capabilities: %w", err)
		}
		for _, c := range []uint{unix.CAP_SETUID, unix.CAP_SETGID} {
			if data[c/32].Effective&(1<<(c%32)) == 0 {
				return nil, fmt.Errorf("switching to %s needs CAP_SETUID and CAP_SETGID, which this program does not hold", name)
			}
		}
	}
	return &accountIDs{UID: uid, GID: gid, Home: u.HomeDir}, nil
}

/*
 * switchAccount makes this thread the account's, keeping its capabilities
 * for dropCapabilities to cut. A process already started as the account (by
 * a privileged program, Options.Start) is left as it is.
 * desc: Raw calls change this thread only, and execve starts the command from
 *       it; Go's and the C library's own change every thread of the program.
 *       Root would lose its capabilities on becoming another account, so it
 *       keeps them (PR_SET_KEEPCAPS) until dropCapabilities.
 */
func switchAccount(a accountIDs) error {
	if unix.Getuid() == a.UID && unix.Getgid() == a.GID {
		return nil
	}
	if os.Geteuid() == 0 {
		if err := unix.Prctl(unix.PR_SET_KEEPCAPS, 1, 0, 0, 0); err != nil {
			return fmt.Errorf("keep capabilities: %w", err)
		}
	}
	if _, _, e := unix.RawSyscall(unix.SYS_SETGROUPS, 0, 0, 0); e != 0 {
		return fmt.Errorf("groups: %w", e)
	}
	g, u := uintptr(a.GID), uintptr(a.UID)
	if _, _, e := unix.RawSyscall(unix.SYS_SETRESGID, g, g, g); e != 0 {
		return fmt.Errorf("group %d: %w", a.GID, e)
	}
	if _, _, e := unix.RawSyscall(unix.SYS_SETRESUID, u, u, u); e != 0 {
		return fmt.Errorf("user %d: %w", a.UID, e)
	}
	return nil
}

/*
 * protectSelf marks this program non-dumpable, so a locked command it starts,
 * running as the same account without CAP_SYS_PTRACE, cannot read its memory,
 * environment or memory map through /proc/<pid>/mem, environ or maps. A
 * command allowed ReadPrograms with CAP_SYS_PTRACE can, so an application
 * that grants it keeps no secret only in memory. The program can still read
 * its own.
 */
func protectSelf() {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		logf("[sandbox] could not make this program non-dumpable: %v", err)
	}
}

func detect() Report {
	if !seccompSupported() {
		return Report{}
	}
	if _, err := unix.PrctlRetInt(unix.PR_GET_SECCOMP, 0, 0, 0, 0); err != nil {
		return Report{}
	}
	abi := landlockABI()
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

func platformCommand(ctx context.Context, cancel bool, r Report, allow map[Effect]bool, writable []string, name string, args []string) (*exec.Cmd, error) {
	cfg := bootConfig{Allow: allow, Writable: writable}
	switch {
	case allow[ChangeFiles]:
	case r.account != nil:
		cfg.Account = r.account
	// Landlock would refuse tracing any program outside the lock.
	case r.LandlockABI > 0 && !allow[ReadPrograms]:
		cfg.LandlockABI = r.LandlockABI
	default:
		cfg.NullFD = true
	}
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
	cmd.Env = append(os.Environ(), childEnv+"=1")
	switch {
	case cfg.Account != nil:
		cmd.Env = append(cmd.Env, "HOME="+cfg.Account.Home, "TMPDIR=/tmp")
	case len(writable) > 0:
		cmd.Env = append(cmd.Env, "TMPDIR="+writable[0])
	}
	return cmd, nil
}

// probeCommand starts the self-test's command; a test replaces it with one that
// does not lock, to show the self-test catches a lock that does not hold.
var probeCommand = platformCommand

// readOnly is a command that may read and may not change files: the account
// holds the writes when there is one; otherwise seccomp does, and with
// ReadPrograms refused, Landlock does where the kernel has it.
func readOnly(readPrograms bool) map[Effect]bool {
	a := DefaultEffects().allowedAt(0)
	a[ChangeFiles], a[ReadPrograms] = false, readPrograms
	return a
}

/*
 * selfTest runs a locked command that reads a file and tries to change others,
 * under each way of holding the writes this system has, and fails unless the
 * read worked and every change was refused.
 */
func selfTest() error {
	if err := probe(readOnly(true)); err != nil {
		return err
	}
	if report.LandlockABI > 0 && report.account == nil {
		return probe(readOnly(false))
	}
	return nil
}

func probe(allow map[Effect]bool) error {
	dir, err := os.MkdirTemp("", "sandbox-selftest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// Readable by the account, which may not hold CAP_DAC_READ_SEARCH.
	if err := os.Chmod(dir, 0o755); err != nil {
		return err
	}
	in, out, keep := filepath.Join(dir, "in"), filepath.Join(dir, "out"), filepath.Join(dir, "keep")
	if err := os.WriteFile(in, []byte("probe"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		return err
	}
	cmd, err := probeCommand(context.Background(), true, report, allow, []string{tmpDir}, "sh",
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
	if st.Mode().Perm() != 0o644 {
		return errors.New("a locked command changed a file's permissions")
	}
	return nil
}
