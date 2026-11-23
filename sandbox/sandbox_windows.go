//go:build windows

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Enter has nothing to do on Windows: the command is started with its rights
// already reduced, with no bootstrap in between.
func Enter() {}

// Low integrity holds ChangeFiles, and with it refuses seeing and signalling
// any program above it: system processes, services and other accounts'
// programs are not even listed (Describe; detect below).
func init() {
	lockedWith[ReadPrograms] = ChangeFiles
	lockedWith[SignalPrograms] = ChangeFiles
	effectWordsHere[ReadPrograms] = "see system and other accounts' programs or read their memory"
}

func checkNotChild() {}

// protectSelf has nothing to do: a low-integrity process cannot open a
// higher-integrity one to read its memory.
func protectSelf() {}

var (
	advapi32                  = windows.NewLazySystemDLL("advapi32.dll")
	procCreateRestrictedToken = advapi32.NewProc("CreateRestrictedToken")

	tokenMu sync.Mutex
	tokens  = map[tokenKind]windows.Token{}

	labelled sync.Map // folders already opened to locked commands

	desktopOnce sync.Once // the window station and desktop opened to the ordinary token
	desktopErr  error

	ordinaryOnce sync.Once // the ordinary token starts a program and reads (selfTestOrdinary)
	ordinaryErr  error
)

// tokenKind is what a command's rights are cut to.
type tokenKind struct {
	low        bool // low integrity: reads every file, changes nothing, sees no program above it
	ordinary   bool // restricting SIDs: reads what an ordinary user may, sees every program, changes nothing
	readFiles  bool // keep SeBackupPrivilege: read any file
	readSecLog bool // keep SeSecurityPrivilege: read the Security event log
}

// keeps reports whether a command keeps a privilege, each kept for the effect
// it serves. SeChangeNotifyPrivilege, to walk folders, is always kept; every
// other privilege of this program's rights is removed.
func (k tokenKind) keeps(name string) bool {
	switch name {
	case "SeChangeNotifyPrivilege":
		return true
	case "SeBackupPrivilege":
		// Never kept: with it a command could reg save the SAM and SECURITY
		// hives. Reading every file at 100 needs only LocalSystem's own rights,
		// which the low-integrity copy keeps for reading.
		return false
	case "SeSecurityPrivilege":
		return k.readSecLog
	}
	return false
}

/*
 * restrictedToken is a copy of this program's rights with every privilege
 * removed except those k keeps, at low integrity when k.low.
 * desc: Low integrity is what refuses changes: a low-integrity process cannot
 *       write to a file, registry key or process at a higher level, which is
 *       nearly all of them, and cannot raise its own level. Everything it
 *       starts inherits the same rights, so no job object is needed to hold
 *       its children: they carry the same token.
 */
func restrictedToken(k tokenKind) (windows.Token, error) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	if t, ok := tokens[k]; ok {
		return t, nil
	}
	t, err := makeToken(k)
	if err != nil {
		return 0, err
	}
	tokens[k] = t
	return t, nil
}

func makeToken(k tokenKind) (windows.Token, error) {
	var self windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_ADJUST_DEFAULT, &self); err != nil {
		return 0, fmt.Errorf("open this program's rights: %w", err)
	}
	defer self.Close()

	privs, err := tokenPrivileges(self)
	if err != nil {
		return 0, fmt.Errorf("list privileges: %w", err)
	}
	var drop []windows.LUIDAndAttributes
	for _, p := range privs {
		name, err := privilegeName(p.Luid)
		if err != nil || !k.keeps(name) {
			drop = append(drop, windows.LUIDAndAttributes{Luid: p.Luid})
		}
	}
	var dropPtr *windows.LUIDAndAttributes
	if len(drop) > 0 {
		dropPtr = &drop[0]
	}
	// One call: Windows starts a program on a token only when it is a
	// restricted copy of this program's own, and a copy of a copy is not.
	var sids []windows.SIDAndAttributes
	if k.ordinary {
		if sids, err = ordinarySIDs(self); err != nil {
			return 0, err
		}
	}
	var sidsPtr *windows.SIDAndAttributes
	if len(sids) > 0 {
		sidsPtr = &sids[0]
	}
	var restricted windows.Token
	r, _, e := procCreateRestrictedToken.Call(uintptr(self), 0,
		0, 0,
		uintptr(len(drop)), uintptr(unsafe.Pointer(dropPtr)),
		uintptr(len(sids)), uintptr(unsafe.Pointer(sidsPtr)),
		uintptr(unsafe.Pointer(&restricted)))
	if r == 0 {
		return 0, fmt.Errorf("remove privileges: %w", e)
	}
	if k.ordinary {
		if err := ordinaryDefaultDACL(self, restricted, sids); err != nil {
			restricted.Close()
			return 0, err
		}
		return restricted, nil
	}
	if !k.low {
		return restricted, nil
	}

	low, err := windows.CreateWellKnownSid(windows.WinLowLabelSid)
	if err != nil {
		restricted.Close()
		return 0, fmt.Errorf("low integrity identifier: %w", err)
	}
	label := windows.Tokenmandatorylabel{Label: windows.SIDAndAttributes{Sid: low, Attributes: windows.SE_GROUP_INTEGRITY}}
	if err := windows.SetTokenInformation(restricted, windows.TokenIntegrityLevel,
		(*byte)(unsafe.Pointer(&label)), uint32(unsafe.Sizeof(label))+windows.GetLengthSid(low)); err != nil {
		restricted.Close()
		return 0, fmt.Errorf("set low integrity: %w", err)
	}
	return restricted, nil
}

/*
 * ordinarySIDs are the level-0 token's restricting SIDs: every access is
 * checked a second time against what they are granted.
 * desc: Level 0 when reading every file is refused: a command reads what an
 *       ordinary user may (System32, most of ProgramData and HKLM; not
 *       another user's profile), is refused every write and every ending of a
 *       process, and still lists every process, which low integrity hides.
 *       Measured as LocalSystem and as a user (Enbarr's
 *       docs/SANDBOX_LEVELS_PLAN.md, step 3). WMI refuses it, so services and
 *       connections come from the fixed read tools.
 *
 *       Everyone, Users, Authenticated Users, RESTRICTED, and this program's
 *       logon session when it has one (a service has none: openDesktop).
 */
func ordinarySIDs(self windows.Token) ([]windows.SIDAndAttributes, error) {
	var sids []windows.SIDAndAttributes
	for _, kind := range []windows.WELL_KNOWN_SID_TYPE{windows.WinWorldSid, windows.WinBuiltinUsersSid,
		windows.WinAuthenticatedUserSid, windows.WinRestrictedCodeSid} {
		sid, err := windows.CreateWellKnownSid(kind)
		if err != nil {
			return nil, fmt.Errorf("restricting identifier: %w", err)
		}
		sids = append(sids, windows.SIDAndAttributes{Sid: sid})
	}
	groups, err := self.GetTokenGroups()
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	for _, g := range groups.AllGroups() {
		if g.Attributes&windows.SE_GROUP_LOGON_ID != 0 {
			sids = append(sids, windows.SIDAndAttributes{Sid: g.Sid})
		}
	}
	return sids, nil
}

/*
 * ordinaryDefaultDACL gives the level-0 token a default DACL that grants this
 * program's user and the restricting SIDs: what a process makes without an
 * inherited DACL (its own process and threads) takes it, and without the
 * restricting SIDs in it the program cannot open itself and fails with
 * 0xc0000142 before it runs.
 */
func ordinaryDefaultDACL(self, t windows.Token, sids []windows.SIDAndAttributes) error {
	user, err := self.GetTokenUser()
	if err != nil {
		return fmt.Errorf("this program's user: %w", err)
	}
	entries := []windows.EXPLICIT_ACCESS{grantAll(user.User.Sid, windows.NO_INHERITANCE)}
	for _, s := range sids {
		entries = append(entries, grantAll(s.Sid, windows.NO_INHERITANCE))
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("default permissions: %w", err)
	}
	dacl := struct{ DefaultDacl *windows.ACL }{acl}
	if err := windows.SetTokenInformation(t, windows.TokenDefaultDacl,
		(*byte)(unsafe.Pointer(&dacl)), uint32(unsafe.Sizeof(dacl))); err != nil {
		return fmt.Errorf("set default permissions: %w", err)
	}
	return nil
}

func grantAll(sid *windows.SID, inherit uint32) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{AccessPermissions: windows.GENERIC_ALL, AccessMode: windows.GRANT_ACCESS, Inheritance: inherit,
		Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeValue: windows.TrusteeValueFromSID(sid)}}
}

var (
	user32                      = windows.NewLazySystemDLL("user32.dll")
	procGetProcessWindowStation = user32.NewProc("GetProcessWindowStation")
	procGetThreadDesktop        = user32.NewProc("GetThreadDesktop")
	procAccessCheck             = advapi32.NewProc("AccessCheck")
)

/*
 * openDesktop grants RESTRICTED this program's window station and desktop,
 * once, when this program has no logon session (a service). A program cannot
 * start without them (0xc0000142). A signed-in user's are granted to the
 * logon session, which is among the restricting SIDs, so they are left alone.
 */
func openDesktop() error {
	desktopOnce.Do(func() {
		if hasLogonSession() {
			return
		}
		restricted, err := windows.CreateWellKnownSid(windows.WinRestrictedCodeSid)
		if err != nil {
			desktopErr = err
			return
		}
		ws, _, e := procGetProcessWindowStation.Call()
		if ws == 0 {
			desktopErr = fmt.Errorf("window station: %w", e)
			return
		}
		dt, _, e := procGetThreadDesktop.Call(uintptr(windows.GetCurrentThreadId()))
		if dt == 0 {
			desktopErr = fmt.Errorf("desktop: %w", e)
			return
		}
		for _, h := range []windows.Handle{windows.Handle(ws), windows.Handle(dt)} {
			sd, err := windows.GetSecurityInfo(h, windows.SE_WINDOW_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				desktopErr = fmt.Errorf("read desktop permissions: %w", err)
				return
			}
			old, _, err := sd.DACL()
			if err != nil {
				desktopErr = err
				return
			}
			acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{grantAll(restricted, windows.NO_INHERITANCE)}, old)
			if err != nil {
				desktopErr = err
				return
			}
			if err := windows.SetSecurityInfo(h, windows.SE_WINDOW_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
				desktopErr = fmt.Errorf("write desktop permissions: %w", err)
				return
			}
		}
	})
	return desktopErr
}

// hasLogonSession reports whether this program's token carries a logon
// session's SID, as a signed-in user's does and LocalSystem's does not.
func hasLogonSession() bool {
	var self windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &self); err != nil {
		return false
	}
	defer self.Close()
	groups, err := self.GetTokenGroups()
	if err != nil {
		return false
	}
	for _, g := range groups.AllGroups() {
		if g.Attributes&windows.SE_GROUP_LOGON_ID != 0 {
			return true
		}
	}
	return false
}

/*
 * openFolder grants RESTRICTED a folder, and everything made in it from now
 * on, so the ordinary token's writes there pass its second check, and nowhere
 * else.
 */
func openFolder(dir string) error {
	if dir == "" {
		return nil
	}
	if _, done := opened.Load(dir); done {
		return nil
	}
	restricted, err := windows.CreateWellKnownSid(windows.WinRestrictedCodeSid)
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read permissions: %w", err)
	}
	old, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read permissions: %w", err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{grantAll(restricted, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)}, old)
	if err != nil {
		return fmt.Errorf("add permission: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return fmt.Errorf("write permissions: %w", err)
	}
	opened.Store(dir, true)
	return nil
}

var opened sync.Map // folders opened to the ordinary token

// fileAllAccess is FILE_ALL_ACCESS (winnt.h), which x/sys/windows does not name.
const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1FF

/*
 * ordinaryMayReadFile reports whether the ordinary token (level 0) may read the
 * opened file: Windows' own access check, made with that token against the
 * permissions read from the file's own handle.
 */
func ordinaryMayReadFile(f *os.File) error {
	tok, err := restrictedToken(tokenKind{ordinary: true})
	if err != nil {
		return err
	}
	var imp windows.Token
	if err := windows.DuplicateTokenEx(tok, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE|windows.TOKEN_DUPLICATE,
		nil, windows.SecurityIdentification, windows.TokenImpersonation, &imp); err != nil {
		return fmt.Errorf("impersonation token: %w", err)
	}
	defer imp.Close()
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.LABEL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read its permissions: %w", err)
	}
	mapping := struct{ Read, Write, Execute, All uint32 }{
		windows.FILE_GENERIC_READ, windows.FILE_GENERIC_WRITE, windows.FILE_GENERIC_EXECUTE, fileAllAccess,
	}
	var privs [64]byte
	privLen := uint32(len(privs))
	var granted, ok uint32
	r, _, e := procAccessCheck.Call(uintptr(unsafe.Pointer(sd)), uintptr(imp), uintptr(windows.FILE_GENERIC_READ),
		uintptr(unsafe.Pointer(&mapping)), uintptr(unsafe.Pointer(&privs[0])), uintptr(unsafe.Pointer(&privLen)),
		uintptr(unsafe.Pointer(&granted)), uintptr(unsafe.Pointer(&ok)))
	if r == 0 {
		return fmt.Errorf("access check: %w", e)
	}
	if ok == 0 {
		return errors.New("its permissions do not let an ordinary user read it")
	}
	return nil
}

/*
 * selfTestOrdinary runs one command on the ordinary token, once: it must start
 * (a service's desktop is opened to it first) and read a file every user may.
 * A failure refuses level-0 commands, not the others.
 */
func selfTestOrdinary(tok windows.Token) error {
	ordinaryOnce.Do(func() {
		hosts := filepath.Join(os.Getenv("SystemRoot"), "System32", "drivers", "etc", "hosts")
		cmd := exec.Command("cmd", "/C", "type", hosts)
		cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(tok), CreationFlags: windows.CREATE_NO_WINDOW}
		out, err := cmd.CombinedOutput()
		if err != nil || len(out) == 0 {
			ordinaryErr = fmt.Errorf("a level-0 command could not start or read %s: %v %q", hosts, err, out)
			logf("[sandbox] %v; level-0 commands are refused", ordinaryErr)
		}
	})
	return ordinaryErr
}

func tokenPrivileges(t windows.Token) ([]windows.LUIDAndAttributes, error) {
	var n uint32
	_ = windows.GetTokenInformation(t, windows.TokenPrivileges, nil, 0, &n)
	if n == 0 {
		return nil, errors.New("no privilege information")
	}
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(t, windows.TokenPrivileges, &buf[0], n, &n); err != nil {
		return nil, err
	}
	tp := (*windows.Tokenprivileges)(unsafe.Pointer(&buf[0]))
	return unsafe.Slice(&tp.Privileges[0], tp.PrivilegeCount), nil
}

var procLookupPrivilegeNameW = advapi32.NewProc("LookupPrivilegeNameW")

func privilegeName(luid windows.LUID) (string, error) {
	buf := make([]uint16, 256)
	n := uint32(len(buf))
	r, _, e := procLookupPrivilegeNameW.Call(0, uintptr(unsafe.Pointer(&luid)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return "", e
	}
	return windows.UTF16ToString(buf[:n]), nil
}

// detect: low integrity refuses ChangeFiles, and with it reading and
// signalling programs above it, which are nearly all; nothing refuses Network,
// and ReadPrograms and SignalPrograms cannot be refused apart from
// ChangeFiles.
//
// Every Windows this runs on has restricted tokens, so a token that cannot be
// made is a failure, not a system without a lock. It used to be reported as no
// lock, which is kept for good and runs every command with the agent's whole
// rights: SYSTEM, for a service. The lock is reported either way, and
// platformCommand makes a token for each command and refuses the command when
// it cannot.
func detect() Report {
	if _, err := restrictedToken(tokenKind{low: true}); err != nil {
		logf("[sandbox] a restricted token could not be made (%v): locked commands are refused until one can", err)
	}
	return Report{Lock: "restricted-token", Unenforced: []Effect{Network}}
}

func platformCommand(ctx context.Context, cancel bool, r Report, allow map[Effect]bool, writable []string, name string, args []string) (*exec.Cmd, error) {
	// Below ChangeFiles: low integrity when every file may be read (it reads
	// them all), the ordinary token when not (it reads what an ordinary user
	// may, by Windows' own permissions).
	readOnly := !allow[ChangeFiles]
	k := tokenKind{low: readOnly && allow[ReadFiles], ordinary: readOnly && !allow[ReadFiles],
		readFiles: allow[ReadFiles], readSecLog: allow[ReadKernelLog]}
	var cmd *exec.Cmd
	if cancel {
		cmd = exec.CommandContext(ctx, name, args...)
	} else {
		cmd = exec.Command(name, args...)
	}
	if !k.low && !k.ordinary && k.readFiles && k.readSecLog {
		return cmd, nil // nothing left to refuse here
	}
	tok, err := restrictedToken(k)
	if err != nil {
		return nil, err
	}
	if k.ordinary {
		if err := openDesktop(); err != nil {
			return nil, fmt.Errorf("open the desktop to level-0 commands: %w", err)
		}
		if err := selfTestOrdinary(tok); err != nil {
			return nil, err
		}
		for _, dir := range writable {
			if err := openFolder(dir); err != nil {
				logf("[sandbox] %s stays unwritable to a level-0 command: %v", dir, err)
			}
		}
		if len(writable) > 0 {
			cmd.Env = append(os.Environ(), "TEMP="+writable[0], "TMP="+writable[0])
		}
	}
	if k.low {
		for _, dir := range writable {
			if err := labelLow(dir); err != nil {
				logf("[sandbox] %s stays unwritable to a locked command: %v", dir, err)
			}
		}
		if len(writable) > 0 {
			cmd.Env = append(os.Environ(), "TEMP="+writable[0], "TMP="+writable[0])
		}
	}
	// CREATE_NO_WINDOW: a console program started from a process with no
	// console (a service: EnbarrNet runs as LocalSystem in session 0) is given
	// a new console, and at low integrity it cannot create one: Windows ends it
	// with 0xC0000142 before it runs, having written nothing. Observed: every
	// locked command, the self-test's included, failing in the service while
	// passing from a console. Without a console window it starts, its output
	// still reaches the pipes, and what it starts inherits the same.
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(tok), CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd, nil
}

/*
 * labelLow gives a folder, and everything made in it from now on, a
 * low-integrity label, so a low-integrity command can write there and
 * nowhere else.
 */
func labelLow(dir string) error {
	if dir == "" {
		return nil
	}
	if _, done := labelled.Load(dir); done {
		return nil
	}
	sd, err := windows.SecurityDescriptorFromString("S:(ML;OICI;NW;;;LW)")
	if err != nil {
		return err
	}
	sacl, _, err := sd.SACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.LABEL_SECURITY_INFORMATION, nil, nil, nil, sacl); err != nil {
		return err
	}
	labelled.Store(dir, true)
	return nil
}

/*
 * selfTest runs one locked command that reads a file and tries to create
 * another, and fails unless the read worked and the create was refused.
 */
func selfTest() error {
	dir, err := os.MkdirTemp("", "sandbox-selftest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	in, out := filepath.Join(dir, "in"), filepath.Join(dir, "out")
	if err := os.WriteFile(in, []byte("probe"), 0o600); err != nil {
		return err
	}
	readOnly := DefaultEffects().allowedAt(0)
	readOnly[ChangeFiles] = false
	cmd, err := platformCommand(context.Background(), true, report, readOnly, nil, "cmd", []string{"/C", "type", in, "&", "echo", "x", ">", out})
	if err != nil {
		return err
	}
	// The command's own exit is non-zero by design (the refused create), so
	// its error is kept only to say why a read failed: a command that never
	// started writes nothing and leaves its exit status as the one clue.
	got, runErr := cmd.Output()
	if len(got) < 5 || string(got[:5]) != "probe" {
		var stderr []byte
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			stderr = ee.Stderr
		}
		return fmt.Errorf("a locked command could not read a file: got %q (%v, stderr %q)", got, runErr, stderr)
	}
	if _, err := os.Stat(out); err == nil {
		return errors.New("a locked command created a file")
	}
	return nil
}

// accountFor: a command runs at low integrity on Windows, not as another
// account, so Options.Account is not used.
func accountFor(string) (*accountIDs, error) { return nil, nil }
