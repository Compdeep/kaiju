package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The snapshot is what the machine is doing rather than what it is, and it is
// the one part of this tool that runs a subprocess — so it is the one part that
// can fail. A failure omits the field; it does not fail the call, because a
// host that will not report its uptime still has a hostname and a clock.
func TestSysinfo_SnapshotIsPresentOrAbsentNeverFatal(t *testing.T) {
	msg, err := NewSysinfo("").ExecuteTyped(context.Background(), nil)
	if err != nil {
		t.Fatalf("sysinfo: %v", err)
	}
	if msg.Status != toolapi.StatusOK {
		t.Fatalf("status = %q, want ok", msg.Status)
	}

	var payload map[string]any
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	for _, always := range []string{"hostname", "os", "arch", "cpus", "time"} {
		if _, ok := payload[always]; !ok {
			t.Errorf("%q is missing and a machine always has one", always)
		}
	}
	// On this platform the snapshot should be there; if it is not, the call
	// still succeeded, which is the property being checked.
	if snap, ok := payload["snapshot"].(string); ok && snap == "" {
		t.Error("snapshot is present and empty — it should be omitted instead")
	}
}

// A nil context used to be harmless because this tool did no I/O. It runs a
// subprocess now, and exec.CommandContext panics on a nil one.
func TestSysinfo_NilContextDoesNotPanic(t *testing.T) {
	if _, err := NewSysinfo("").ExecuteTyped(nil, nil); err != nil {
		t.Fatalf("sysinfo with a nil context: %v", err)
	}
}
