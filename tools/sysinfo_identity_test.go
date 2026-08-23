package tools

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A run refused by the operating system had symptoms and no cause: permission
// denied under /etc, command not found for a program installed in /usr/sbin,
// and a service it could not restart. One fact explains all three, and nothing
// reported it — so the run concluded the software was missing when it was
// installed and running.
func TestSysinfo_ReportsWhoThisProcessIs(t *testing.T) {
	msg, err := (&Sysinfo{}).ExecuteTyped(nil, nil)
	if err != nil {
		t.Fatalf("sysinfo: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(msg.Data, &got); err != nil {
		t.Fatalf("payload unreadable: %v", err)
	}

	if u, ok := got["user"].(string); !ok || u == "" {
		t.Fatalf("the account this runs as must be reported, got %v", got["user"])
	}
	if p, ok := got["path"].(string); !ok || p == "" {
		t.Fatalf("PATH must be reported, since command-not-found is answered by it, got %v", got["path"])
	}

	// The question has no answer on Windows, so it is left out there rather than
	// answered wrongly.
	if runtime.GOOS == "windows" {
		if _, present := got["root"]; present {
			t.Fatal("windows has no uid, so root must not be claimed either way")
		}
		return
	}
	isRoot, ok := got["root"].(bool)
	if !ok {
		t.Fatalf("root must be stated on this platform, got %v", got["root"])
	}
	if isRoot != (os.Geteuid() == 0) {
		t.Fatalf("root = %v but euid is %d", isRoot, os.Geteuid())
	}
	if uid, ok := got["uid"].(float64); !ok || int(uid) != os.Geteuid() {
		t.Fatalf("uid = %v, want %d", got["uid"], os.Geteuid())
	}
	t.Logf("this host: user=%v uid=%v root=%v", got["user"], got["uid"], got["root"])
	t.Logf("PATH includes /usr/sbin: %v", strings.Contains(got["path"].(string), "/usr/sbin"))
}

// Every field the tool returns must be declared, or a step cannot reference it
// and a planner is never told it exists.
func TestSysinfo_DeclaresEveryFieldItReturns(t *testing.T) {
	msg, _ := (&Sysinfo{}).ExecuteTyped(nil, nil)
	var got map[string]any
	_ = json.Unmarshal(msg.Data, &got)

	payload := toolapi.PayloadSchema((&Sysinfo{}).OutputSchema())
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("output schema unreadable: %v", err)
	}
	for field := range got {
		if _, declared := parsed.Properties[field]; !declared {
			t.Errorf("sysinfo returns %q and declares nothing about it", field)
		}
	}
}
