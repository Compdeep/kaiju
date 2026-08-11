package tools

import (
	"fmt"
	"net"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// Clearing a port must only ever kill a process this tool started.
//
// It used to run `fuser -k <port>/tcp`. On a machine somebody else is using,
// that is their database or their editor's language server, killed without a
// word by a tool asked to start something unrelated.

// listenerOnAnyPort starts a process that holds a TCP port and returns the pid,
// the port, the exact command line it is running, and a way to stop it.
//
// It binds and listens but never accepts, because portOpen checks liveness by
// dialling: a listener that serves one connection and exits is killed by the
// check meant to confirm it is there. exec replaces the shell so the pid is the
// listener itself and not a parent that outlives it. Both of those were wrong
// in the first version of this helper, and both made the tests below pass
// without holding a port at all.
func listenerOnAnyPort(t *testing.T) (pid, port int, command string, stop func()) {
	t.Helper()
	// Take a port, learn its number, release it for the child to bind.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	port = l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	script := fmt.Sprintf(
		"import socket,time;s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);"+
			"s.bind((\"127.0.0.1\",%d));s.listen(8);time.sleep(120)", port)
	command = "python3 -c " + script

	cmd := exec.Command("sh", "-c", "exec python3 -c '"+script+"'")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start a listener: %v", err)
	}
	pid = cmd.Process.Pid

	// The port must actually be held, or nothing below means anything.
	held := false
	for i := 0; i < 40 && !held; i++ {
		time.Sleep(50 * time.Millisecond)
		held = portOpen(port)
	}
	if !held {
		_ = signalGroup(pid, syscall.SIGKILL)
		t.Skipf("could not hold port %d, so there is nothing to clear", port)
	}
	return pid, port, script, func() {
		_ = signalGroup(pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	}
}

// A process this tool started, recorded in its registry, is cleared.
func TestFreePortClearsOurOwnProcess(t *testing.T) {
	s := &Service{workspace: t.TempDir()}
	pid, port, command, stop := listenerOnAnyPort(t)
	defer stop()

	if err := s.saveRegistry([]ServiceRecord{{
		Name: "ours", Command: command,
		Port: port, PID: pid,
	}}); err != nil {
		t.Fatalf("write the registry: %v", err)
	}

	s.freePort(port)

	if isAlive(pid) {
		t.Error("a process this tool started and recorded was left running, so the " +
			"spawn that follows cannot bind")
	}
}

// A process nobody recorded is left alone, whatever it is holding.
func TestFreePortLeavesAStrangersProcessAlone(t *testing.T) {
	s := &Service{workspace: t.TempDir()}
	pid, port, _, stop := listenerOnAnyPort(t)
	defer stop()

	// An empty registry: this tool started nothing.
	if err := s.saveRegistry(nil); err != nil {
		t.Fatalf("write the registry: %v", err)
	}

	s.freePort(port)

	if !isAlive(pid) {
		t.Error("a process this tool never started was killed for holding a port — " +
			"on a monitored machine that is the customer's own software")
	}
}

// A recorded pid that now belongs to something else is left alone. Pids are
// reused and the registry outlives a reboot, so the number alone proves nothing.
func TestFreePortLeavesAReusedPidAlone(t *testing.T) {
	s := &Service{workspace: t.TempDir()}
	pid, port, _, stop := listenerOnAnyPort(t)
	defer stop()

	if err := s.saveRegistry([]ServiceRecord{{
		Name: "long-gone", Command: "a-command-this-process-is-not-running",
		Port: port, PID: pid,
	}}); err != nil {
		t.Fatalf("write the registry: %v", err)
	}

	s.freePort(port)

	if !isAlive(pid) {
		t.Error("a pid was signalled on the strength of the number alone; after a " +
			"reboot that number belongs to a stranger")
	}
}
