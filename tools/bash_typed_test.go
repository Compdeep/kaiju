package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

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

// The timeout is idle, not wall clock: a command doing its job is not killed
// for taking a while, and a command doing nothing is.
//
// This is what a download is. wget on a large file runs for minutes and prints
// the whole time; a wall clock kills it for succeeding slowly. What was there
// before guessed instead — it matched the command text for "wget", "npm
// install" and a dozen others and gave those a longer clock, which is wrong for
// everything not on the list.
func TestBash_IdleTimeoutSparesAWorkingCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shell script here is sh")
	}
	b := NewBash("", "")

	// Runs for 2 seconds, prints every 200ms. An idle limit of 1s must not kill
	// it: it is never quiet for a whole second.
	start := time.Now()
	msg, err := b.ExecuteTyped(context.Background(), map[string]any{
		"command":     "for i in $(seq 1 10); do echo tick $i; sleep 0.2; done",
		"timeout_sec": 1,
	})
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if msg.Status != toolapi.StatusOK {
		t.Fatalf("a command printing every 200ms was killed by a 1s idle limit: %q %q", msg.Status, msg.Detail)
	}
	if !strings.Contains(msg.Content, "tick 10") {
		t.Errorf("it did not run to the end:\n%s", msg.Content)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("it returned in %s — it cannot have run the full 2 seconds", elapsed)
	}
}

// And the other half: silence is what gets killed.
func TestBash_IdleTimeoutKillsAStuckCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shell script here is sh")
	}
	start := time.Now()
	msg, err := NewBash("", "").ExecuteTyped(context.Background(), map[string]any{
		"command":     "echo starting; sleep 30",
		"timeout_sec": 1,
	})
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if msg.Status != toolapi.StatusError {
		t.Fatalf("a command silent for 30s was not killed: %q", msg.Status)
	}
	if !strings.Contains(msg.Detail, "no output") {
		t.Errorf("the detail should say why it was killed, got %q", msg.Detail)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("it took %s to notice a 1s idle limit", elapsed)
	}
	// What it did print before going quiet is kept — it is often the only clue.
	var fields map[string]any
	if err := json.Unmarshal(msg.Data, &fields); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if out, _ := fields["stdout"].(string); !strings.Contains(out, "starting") {
		t.Errorf("what it printed before stalling was dropped: %v", fields)
	}
}
