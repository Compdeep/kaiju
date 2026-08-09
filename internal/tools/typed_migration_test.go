package tools

import (
	"testing"

	agenttools "github.com/Compdeep/kaiju/agent/tools"
)

// A migrated tool has to be visible as typed to the dispatcher, which checks
// for this interface and nothing else. A rename that leaves the interface
// unsatisfied is inert and silent, so it is asserted rather than assumed.
func TestMigratedToolsSatisfyTypedExecutor(t *testing.T) {
	migrated := map[string]agenttools.Tool{
		"bash":      NewBash(""),
		"web_fetch": NewWebFetch(),
		"file_read": NewFileRead(""),
	}
	for name, tool := range migrated {
		if _, ok := tool.(agenttools.TypedExecutor); !ok {
			t.Errorf("%s does not implement TypedExecutor — the dispatcher will take the string path", name)
		}
	}
}

// And the string path still works for callers outside the DAG, which is the
// only reason Execute still exists on them.
func TestMigratedToolsStillReturnAString(t *testing.T) {
	out, err := NewFileRead(t.TempDir()).Execute(nil, map[string]any{"path": "absent"})
	if err == nil {
		t.Fatal("want the read error")
	}
	if out != "" {
		t.Fatalf("an error must carry no result, got %q", out)
	}
}
