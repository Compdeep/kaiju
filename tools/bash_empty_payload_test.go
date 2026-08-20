package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A command that prints nothing must not have its own text read back as
// something the run established.
//
// The case this comes from: a planner wrote a script of one comment and one
// print statement, the shell exited 0 with no output, and the sentence inside
// the print statement was quoted into Detail. Detail is prose — a later stage
// reads it as something the run established — so that sentence came back as
// "the run has confirmed that direct calculation is not feasible" and then as
// the reason given to the user for not answering.
func TestBashEmptyResult_KeepsTheCommandOutOfTheProse(t *testing.T) {
	const sentence = "this machine cannot possibly do arithmetic"
	command := `python3 -c "# a comment\nprint('` + sentence + `')" > /dev/null`

	b := NewBash("", t.TempDir())
	msg, err := b.ExecuteTyped(context.Background(), map[string]any{"command": command})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if msg.Status != toolapi.StatusEmpty {
		t.Fatalf("status = %q, want %q — this test is not exercising the empty path", msg.Status, toolapi.StatusEmpty)
	}

	// The prose the model reads.
	if strings.Contains(msg.Detail, sentence) {
		t.Errorf("the command's own text is in Detail, which a later stage reads as something the run established: %q", msg.Detail)
	}
	if strings.Contains(msg.Detail, "python3") {
		t.Errorf("Detail names the command rather than saying why nothing came back: %q", msg.Detail)
	}

	// And it is still recorded, where references and the frontend read it.
	var data struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		t.Fatalf("the payload is not readable, so the command was not recorded anywhere: %v", err)
	}
	if !strings.Contains(data.Command, sentence) {
		t.Errorf("the command is not in the payload either — it has been lost, not moved: %q", data.Command)
	}
}
