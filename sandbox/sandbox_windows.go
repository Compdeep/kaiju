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

func checkNotChild() {}

// protectSelf has nothing to do: a low-integrity process cannot open a
// higher-integrity one to read its memory.
func protectSelf() {}

var (
	advapi32                  = windows.NewLazySystemDLL("advapi32.dll")
	procCreateRestrictedToken = advapi32.NewProc("CreateRestrictedToken")

	tokenMu sync.Mutex
	tokens  = map[tokenKind]windows.Token{}

	labelled sync.Map // folders already labelled low integrity
)

// tokenKind is what a command's rights are cut to.
type tokenKind struct {
	low        bool // low integrity: no change to files, other programs or the registry
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
		return k.readFiles
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
	var restricted windows.Token
	r, _, e := procCreateRestrictedToken.Call(uintptr(self), 0,
		0, 0,
		uintptr(len(drop)), uintptr(unsafe.Pointer(dropPtr)),
		0, 0,
		uintptr(unsafe.Pointer(&restricted)))
	if r == 0 {
		return 0, fmt.Errorf("remove privileges: %w", e)
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
func detect() Report {
	if _, err := restrictedToken(tokenKind{low: true}); err != nil {
		return Report{}
	}
	return Report{Lock: "restricted-token", Unenforced: []Effect{Network}}
}

func platformCommand(ctx context.Context, cancel bool, r Report, allow map[Effect]bool, writable []string, name string, args []string) (*exec.Cmd, error) {
	k := tokenKind{low: !allow[ChangeFiles], readFiles: allow[ReadFiles], readSecLog: allow[ReadKernelLog]}
	var cmd *exec.Cmd
	if cancel {
		cmd = exec.CommandContext(ctx, name, args...)
	} else {
		cmd = exec.Command(name, args...)
	}
	if !k.low && k.readFiles && k.readSecLog {
		return cmd, nil // nothing left to refuse here
	}
	tok, err := restrictedToken(k)
	if err != nil {
		return nil, err
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(tok)}
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
	got, _ := cmd.Output()
	if len(got) < 5 || string(got[:5]) != "probe" {
		return fmt.Errorf("a locked command could not read a file: got %q", got)
	}
	if _, err := os.Stat(out); err == nil {
		return errors.New("a locked command created a file")
	}
	return nil
}
