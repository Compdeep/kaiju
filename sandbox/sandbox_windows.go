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

var (
	advapi32                  = windows.NewLazySystemDLL("advapi32.dll")
	procCreateRestrictedToken = advapi32.NewProc("CreateRestrictedToken")

	tokenOnce sync.Once
	lowToken  windows.Token
	tokenErr  error
)

// keepPrivileges are the privileges a locked command keeps: the ones that only
// read. Every other privilege of this program's rights is removed.
var keepPrivileges = map[string]bool{
	"SeChangeNotifyPrivilege": true, // walk folders the command may read
	"SeBackupPrivilege":       true, // read any file
	"SeSecurityPrivilege":     true, // read the Security event log
}

/*
 * readOnlyToken is a copy of this program's rights at low integrity, with
 * every privilege removed except keepPrivileges.
 * desc: Low integrity is what refuses writes: a low-integrity process cannot
 *       write to a file, registry key or process at a higher level, which is
 *       nearly all of them, and cannot raise its own level. Everything it
 *       starts inherits the same rights, so no job object is needed to hold
 *       its children: they carry the same token.
 */
func readOnlyToken() (windows.Token, error) {
	tokenOnce.Do(func() {
		var self windows.Token
		if err := windows.OpenProcessToken(windows.CurrentProcess(),
			windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_ADJUST_DEFAULT, &self); err != nil {
			tokenErr = fmt.Errorf("open this program's rights: %w", err)
			return
		}
		defer self.Close()

		var drop []windows.LUIDAndAttributes
		if privs, err := tokenPrivileges(self); err == nil {
			for _, p := range privs {
				name, err := privilegeName(p.Luid)
				if err != nil || !keepPrivileges[name] {
					drop = append(drop, windows.LUIDAndAttributes{Luid: p.Luid})
				}
			}
		} else {
			tokenErr = fmt.Errorf("list privileges: %w", err)
			return
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
			tokenErr = fmt.Errorf("remove privileges: %w", e)
			return
		}

		low, err := windows.CreateWellKnownSid(windows.WinLowLabelSid)
		if err != nil {
			restricted.Close()
			tokenErr = fmt.Errorf("low integrity identifier: %w", err)
			return
		}
		label := windows.Tokenmandatorylabel{Label: windows.SIDAndAttributes{Sid: low, Attributes: windows.SE_GROUP_INTEGRITY}}
		if err := windows.SetTokenInformation(restricted, windows.TokenIntegrityLevel,
			(*byte)(unsafe.Pointer(&label)), uint32(unsafe.Sizeof(label))+windows.GetLengthSid(low)); err != nil {
			restricted.Close()
			tokenErr = fmt.Errorf("set low integrity: %w", err)
			return
		}
		lowToken = restricted
	})
	return lowToken, tokenErr
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

func detect() Report {
	if _, err := readOnlyToken(); err != nil {
		return Report{}
	}
	return Report{Lock: "restricted-token"}
}

func platformCommand(ctx context.Context, cancel bool, r Report, writable []string, name string, args []string) (*exec.Cmd, error) {
	tok, err := readOnlyToken()
	if err != nil {
		return nil, err
	}
	var cmd *exec.Cmd
	if cancel {
		cmd = exec.CommandContext(ctx, name, args...)
	} else {
		cmd = exec.Command(name, args...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(tok)}
	return cmd, nil
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
	cmd, err := platformCommand(context.Background(), true, report, nil, "cmd", []string{"/C", "type", in, "&", "echo", "x", ">", out})
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
