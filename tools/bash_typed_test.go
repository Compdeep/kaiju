package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

func TestBash_TypedEnvelope(t *testing.T) {
	b := NewBash("")

	// success → ok envelope, exit_code 0, stdout in data
	ok, err := b.ExecuteTyped(context.Background(), map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("echo errored: %v", err)
	}
	if ok.Type != "command" || ok.Status != toolapi.StatusOK {
		t.Fatalf("echo → kind %q status %q", ok.Type, ok.Status)
	}
	var d struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
	}
	_ = json.Unmarshal(ok.Data, &d)
	if d.ExitCode != 0 || !strings.Contains(d.Stdout, "hello") {
		t.Fatalf("echo data = %+v", d)
	}

	// nonzero exit → error envelope with a NIL Go error (so the node resolves and
	// the scheduler detects the failure from Status, driving self-repair)
	bad, err := b.ExecuteTyped(context.Background(), map[string]any{"command": "exit 3"})
	if err != nil {
		t.Fatalf("nonzero exit should be a nil Go error, got: %v", err)
	}
	if bad.Status != toolapi.StatusError {
		t.Fatalf("exit 3 → status %q want error", bad.Status)
	}
	var d2 struct {
		ExitCode int `json:"exit_code"`
	}
	_ = json.Unmarshal(bad.Data, &d2)
	if d2.ExitCode != 3 {
		t.Fatalf("exit 3 → exit_code %d want 3", d2.ExitCode)
	}
}

// A command that succeeded and printed nothing is the commonest way a shell
// says "not there" — grep with no match, find with no file. Reporting it as a
// result leaves the next step to infer the absence from an empty string, which
// reads the same as a command whose output was lost.
func TestBash_SilentSuccessIsAnAbsence(t *testing.T) {
	dir := t.TempDir()
	msg, err := NewBash("", "").ExecuteTyped(context.Background(),
		map[string]any{"command": "find " + dir + " -name zzz-no-such-file"})
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if msg.Status != toolapi.StatusEmpty {
		t.Fatalf("a command that printed nothing = %q (%q), want empty", msg.Status, msg.Content)
	}
	if !strings.Contains(msg.Detail, "printed nothing") {
		t.Errorf("the detail should say what happened, got %q", msg.Detail)
	}

	// And a command that did print something is still a result.
	msg, err = NewBash("", "").ExecuteTyped(context.Background(),
		map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if msg.Status != toolapi.StatusOK || !strings.Contains(msg.Content, "hello") {
		t.Errorf("echo hello = %q %q, want ok carrying the output", msg.Status, msg.Content)
	}
}
