//go:build windows

package tools

import (
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"
)

// The same six questions, answered the way Windows answers them.
//
// Windows has no signals and no process groups that can be polled, so none of the unix
// calls exist here. What it has instead: a creation flag that makes a process the root
// of a group, taskkill with /T to end a process and everything below it, and a process
// handle whose exit code says whether it has finished.
//
// taskkill rather than an API call for the tree, because walking the process tree by
// hand means enumerating every process on the machine and matching parents — which is
// the job taskkill already does, and which the rest of this product already relies on
// for the same purpose.

const (
	// createNewProcessGroup makes the new process the root of a group, which is what
	// lets its children be ended with it.
	createNewProcessGroup = 0x00000200
	// stillActive is what GetExitCodeProcess reports for a process that has not
	// finished. It is a real exit code as well, so a process that exits with 259
	// reads as running until its handle is closed — a corner nothing here can fix
	// and the documented behaviour of the call.
	stillActive = 259
	// processQueryLimitedInformation is enough to ask whether a process has ended,
	// and less than the right to read or change it.
	processQueryLimitedInformation = 0x1000
)

var (
	kernel32DLL         = syscall.NewLazyDLL("kernel32.dll")
	openProcessProc     = kernel32DLL.NewProc("OpenProcess")
	getExitCodeProcProc = kernel32DLL.NewProc("GetExitCodeProcess")
	closeHandleProc     = kernel32DLL.NewProc("CloseHandle")
)

/*
 * setOwnProcessGroup makes a command the root of its own process group, so its children
 * can be ended along with it.
 *
 * param: cmd - the command, before it is started.
 */
func setOwnProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

/*
 * setOwnSession is the same thing here.
 *
 * Windows has no sessions in the unix sense — there is no controlling terminal to
 * detach from — so the group flag is the whole of what this can mean. Kept as a
 * separate name because the callers mean two different things by it and one of them
 * would be wrong to change on the platform where the difference is real.
 *
 * param: cmd - the command, before it is started.
 */
func setOwnSession(cmd *exec.Cmd) {
	setOwnProcessGroup(cmd)
}

/*
 * stopProcessTree asks the process and everything below it to close.
 *
 * Without /F, taskkill asks a process to close rather than ending it, which is the
 * nearest thing to a termination signal: a program that handles it gets to shut down
 * cleanly, and one that ignores it is still running when the caller gives up and calls
 * killProcessTree.
 *
 * param: pid - the process at the root of the tree.
 * return: nil when there was nothing left to stop, or the error from taskkill.
 */
func stopProcessTree(pid int) error { return taskkill(pid, false) }

/*
 * killProcessTree ends the process and everything below it.
 *
 * param: pid - the process at the root of the tree.
 * return: nil when there was nothing left to end, or the error from taskkill.
 */
func killProcessTree(pid int) error { return taskkill(pid, true) }

// taskkill runs the platform's own tree-ending command. A process that is already gone
// is not an error: the caller asked for it to be stopped and it is stopped.
func taskkill(pid int, force bool) error {
	if pid <= 0 {
		return nil
	}
	args := []string{"/T", "/PID", strconv.Itoa(pid)}
	if force {
		args = append(args, "/F")
	}
	out, err := exec.Command("taskkill", args...).CombinedOutput()
	if err != nil {
		if !processIsAlive(pid) {
			return nil
		}
		return fmt.Errorf("taskkill %d: %w: %s", pid, err, out)
	}
	return nil
}

/*
 * processIsAlive reports whether a process exists and has not finished.
 *
 * param: pid - the process to ask about.
 * return: true when it is running.
 */
func processIsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, _, _ := openProcessProc.Call(
		uintptr(processQueryLimitedInformation), 0, uintptr(pid))
	if handle == 0 {
		return false
	}
	defer closeHandleProc.Call(handle)

	var code uint32
	r, _, _ := getExitCodeProcProc.Call(handle, uintptr(unsafe.Pointer(&code)))
	if r == 0 {
		// The handle opened, so something is there; the exit code could not be
		// read. Reporting it as running is the safer of the two wrong answers.
		return true
	}
	return code == stillActive
}

/*
 * treeIsAlive reports whether the tree led by pid still has anything in it.
 *
 * Windows offers no way to ask about a group the way a signal to a negative pid does on
 * unix, so this answers for the process at the root. That is the honest limit: a root
 * that has ended while a child of it has not will read as finished here, and the
 * caller's next step ends the tree by name anyway.
 *
 * param: pid - the process at the root of the tree.
 * return: true when the root is still running.
 */
func treeIsAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	return processIsAlive(pid)
}

/*
 * reapProcess does nothing here.
 *
 * A finished process on unix keeps its entry until its parent collects it, and that
 * entry makes a group look occupied. Windows has no equivalent: a process that has
 * ended is gone, and only the handle remains, which is closed where it is opened.
 *
 * param: pid - unused.
 */
func reapProcess(pid int) {}
