package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Compdeep/kaiju/agent/toolapi"
	"os"
	"strings"
	"testing"
	"time"
)

// Stopping a service stops the process it started.
//
// start runs the command through `sh -c` in a session of its own, so the pid on
// the record is the shell's and the command runs as its child. Signalling that
// one pid killed the shell and left the command running — reparented to init,
// still bound to whatever port it had, and no longer named in any registry,
// while the tool reported "stopped".
//
// Those are the orphans freePort exists to clear, and this is where they came
// from: every stop made one, and the next start on the same port killed it by
// searching for whatever held the port. The two covered for each other, which is
// why nothing looked wrong. A service with no port declared was simply left
// running.
//
// The tool had no tests of its own before this one.

func TestStoppingAServiceStopsTheProcess(t *testing.T) {
	s, ws := newTestService(t)

	// The plainest command there is. /bin/sh forks it rather than execing it —
	// measured, not assumed — so the shell the tool records is not the process
	// doing the work. Nothing here is arranged to make that true.
	msg, err := s.start(map[string]any{
		"name": "web", "command": "sleep 300", "workdir": ws,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var started struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(msg.Data, &started); err != nil {
		t.Fatalf("read start payload: %v", err)
	}

	// The fork is not instant. Wait for it rather than reading /proc once and
	// concluding there is no child to strand.
	var children []int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if children = childrenOf(started.PID); len(children) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(children) == 0 {
		t.Fatal("the shell never started the command")
	}
	// Kept so a failure does not leave the process behind, which is the whole
	// subject of the test.
	t.Cleanup(func() {
		for _, pid := range children {
			_ = killProcessTree(pid)
		}
	})

	if _, err := s.stop(map[string]any{"name": "web"}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	for _, pid := range children {
		if processIsAlive(pid) {
			t.Errorf("stop reported the service stopped and pid %d is still running. "+
				"It is the command itself: the shell was signalled and the process it "+
				"started outlived it, still holding whatever port it bound", pid)
		}
	}
}

// A service that has already gone is not an error to stop, and stopping it does
// not go looking for a process group that is no longer there.
func TestStoppingAServiceThatHasAlreadyDiedIsQuiet(t *testing.T) {
	s, ws := newTestService(t)

	msg, err := s.start(map[string]any{
		"name": "web", "command": "sleep 300", "workdir": ws,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var started struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(msg.Data, &started); err != nil {
		t.Fatalf("read start payload: %v", err)
	}

	if _, err := s.stop(map[string]any{"name": "web"}); err != nil {
		t.Fatalf("first stop: %v", err)
	}
	if _, err := s.stop(map[string]any{"name": "web"}); err != nil {
		t.Errorf("stopping an already-stopped service reported an error: %v", err)
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

// newTestService builds a service manager over a temporary workspace, stops its
// health loop when the test ends, and kills anything it started. The loop is the
// only thing the manager starts on its own and nothing else stops it.
func newTestService(t *testing.T) (*Service, string) {
	t.Helper()
	ws := t.TempDir()
	s := NewService(ws)
	t.Cleanup(s.StopPolling)
	t.Cleanup(func() {
		recs, err := s.loadRegistry()
		if err != nil {
			return
		}
		for _, r := range recs {
			if r.PID > 1 {
				_ = killProcessTree(r.PID)
			}
		}
	})
	return s, ws
}

// childrenOf lists the immediate children of pid, or nothing when it is gone.
func childrenOf(pid int) []int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", pid, pid))
	if err != nil {
		return nil
	}
	var out []int
	for _, f := range strings.Fields(string(data)) {
		var child int
		if _, err := fmt.Sscanf(f, "%d", &child); err == nil {
			out = append(out, child)
		}
	}
	return out
}

// A service nobody registered is an answer, not a failure.
//
// Asking about one is how a caller finds out whether it is there, and every action
// that names a service raised a Go error instead — which ends the step and takes the
// answer with it.
func TestServiceOnANameThatIsNotRegistered(t *testing.T) {
	svc := NewService(t.TempDir())
	for _, action := range []string{"status", "logs", "stop", "restart", "remove"} {
		msg, err := svc.ExecuteTyped(context.Background(),
			map[string]any{"action": action, "name": "no-such-service"})
		if err != nil {
			t.Errorf("%s returned a Go error rather than a result: %v", action, err)
			continue
		}
		if msg.Status != toolapi.StatusEmpty {
			t.Errorf("%s = %q, want empty", action, msg.Status)
		}
		if !strings.Contains(msg.Detail, "no-such-service") {
			t.Errorf("%s does not name the service: %q", action, msg.Detail)
		}
	}
}
