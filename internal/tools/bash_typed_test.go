package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agenttools "github.com/Compdeep/kaiju/agent/tools"
)

func TestBash_TypedEnvelope(t *testing.T) {
	b := NewBash("")

	// success → ok envelope, exit_code 0, stdout in data
	ok, err := b.ExecuteTyped(context.Background(), map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("echo errored: %v", err)
	}
	if ok.Type != "command" || ok.Status != agenttools.StatusOK {
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
	if bad.Status != agenttools.StatusError {
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
