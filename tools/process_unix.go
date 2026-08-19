//go:build !windows

package tools

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Starting a process so its children can be stopped with it, and stopping them.
//
// A command that starts other commands — a shell running `npx vite &`, a service that
// forks a worker — leaves those behind when only the process this package started is
// killed. Every platform solves that, and no two solve it the same way, so the whole
// of the difference lives in this file and its Windows counterpart. Everything above
// them is one implementation.
//
// This is why the package stopped building for Windows: these calls were written
// inline in bash.go and service.go, where the compiler tries them on every platform.
// `syscall.Kill` does not exist on Windows, which has no signals, and
// `syscall.SysProcAttr` is a different structure there with none of the same fields.

/*
 * setOwnProcessGroup makes a command lead its own process group, so its children can be
 * stopped along with it.
 *
 * param: cmd - the command, before it is started.
 */
func setOwnProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

/*
 * setOwnSession makes a command lead its own session as well as its own group.
 *
 * A session leader has no controlling terminal, which is what a long-running service
 * wants: it does not die when whatever started it goes away.
 *
 * param: cmd - the command, before it is started.
 */
func setOwnSession(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

/*
 * stopProcessTree asks every process in the tree led by pid to stop.
 *
 * A negative pid addresses the group. When there is no group — a record written before
 * the leader existed, or a process that never led one — the process is signalled on its
 * own, which is what this did for everything before groups were used.
 *
 * param: pid - the group leader.
 * return: nil when the signal was delivered or there was nothing left to signal.
 */
func stopProcessTree(pid int) error { return signalTree(pid, syscall.SIGTERM) }

/*
 * killProcessTree makes every process in the tree led by pid stop.
 *
 * param: pid - the group leader.
 * return: nil when the signal was delivered or there was nothing left to signal.
 */
func killProcessTree(pid int) error { return signalTree(pid, syscall.SIGKILL) }

// signalTree sends one signal to the group, falling back to the process alone.
func signalTree(pid int, sig syscall.Signal) error {
	err := syscall.Kill(-pid, sig)
	if err == nil {
		return nil
	}
	if err != syscall.ESRCH {
		return err
	}
	if err := syscall.Kill(pid, sig); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

/*
 * processIsAlive reports whether a process exists, is ours to signal, and has not
 * already finished.
 *
 * Signal 0 asks whether the process is there without sending anything. That alone is
 * not enough: a child that has exited and not been collected keeps its entry until its
 * parent collects it, and answers signal 0 while being dead — so the state is read as
 * well, and Z is not alive.
 *
 * param: pid - the process to ask about.
 * return: true when it is running.
 */
func processIsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		// The state could not be read, and signal 0 succeeded. Reporting it as
		// running is the safer of the two wrong answers: the caller then tries to
		// stop something already gone, rather than leaving something running.
		return true
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "State:") {
			return !strings.Contains(line, "Z")
		}
	}
	return true
}

/*
 * treeIsAlive reports whether anything is left in the tree led by pid.
 *
 * Signal 0 asks the question without sending anything, and being refused permission is
 * still an answer that something is there.
 *
 * param: pid - the group leader.
 * return: true when at least one process remains.
 */
func treeIsAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(-pid, 0)
	return err == nil || err == syscall.EPERM
}

/*
 * reapProcess collects a child that has finished.
 *
 * A process started here and never waited on keeps its entry until this process exits.
 * That entry is still a member of its group, so without this the group reads as
 * occupied for the whole timeout and every stop takes as long as it is allowed to. A
 * process inherited from an earlier run is not our child; the error says so and there
 * is nothing to do about it, because it is collected elsewhere.
 *
 * param: pid - the child to collect.
 */
func reapProcess(pid int) {
	var status syscall.WaitStatus
	_, _ = syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
}
