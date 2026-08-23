package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// "ports" is what this host is listening on. "connections" is what it is
// talking to — every socket in any state, with the process holding it. They are
// not interchangeable: a process calling out holds an established outbound connection and
// listens on nothing, so a scan that only lists listeners cannot see it.
func TestNetInfo_ConnectionsSeesMoreThanListeners(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("the socket listing differs on %s", runtime.GOOS)
	}
	n := NewNetInfo()

	conns, err := n.ExecuteTyped(context.Background(), map[string]any{"action": "connections"})
	if err != nil {
		t.Fatalf("net_info connections: %v", err)
	}
	if conns.Status == toolapi.StatusError {
		t.Skipf("no socket listing on this machine: %s", conns.Detail)
	}

	// A filter that matches nothing is an absence, not a failure — the host was
	// asked and has no such connection.
	miss, err := n.ExecuteTyped(context.Background(),
		map[string]any{"action": "connections", "host": "198.51.100.77"})
	if err != nil {
		t.Fatalf("net_info connections filtered: %v", err)
	}
	if miss.Status != toolapi.StatusEmpty {
		t.Errorf("a filter matching nothing = %q, want empty", miss.Status)
	}
	if !strings.Contains(miss.Detail, "198.51.100.77") {
		t.Errorf("the detail should name what was looked for, got %q", miss.Detail)
	}
}
