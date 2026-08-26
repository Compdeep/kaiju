package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A stage's whole view of an earlier step is the text Evidence returns. Anything
// in the payload was reachable by reference and discoverable by nobody — so a
// tool that kept the rest of its output in a file was keeping it for a reader
// who would never learn the file existed.
//
// Read from a run that had been told a path exists, was given none, invented one
// from the URL, and failed on a file that was never there.

func bodyWith(t *testing.T, msg toolapi.ToolMessage) toolMessageBody {
	t.Helper()
	return toolMessageBody{msg: msg}
}

func TestEvidence_NamesTheFileThePayloadPointsAt(t *testing.T) {
	b := bodyWith(t, toolapi.ToolOK("page", "the first part of a long document", map[string]any{
		"full_content_path": "fetched/example_123",
		"bytes":             1219043,
	}))

	ev := b.Evidence()
	if !strings.Contains(ev, "the first part of a long document") {
		t.Error("the content itself is missing")
	}
	if !strings.Contains(ev, "fetched/example_123") {
		t.Errorf("the file is not named, so a later step cannot know it exists:\n%s", ev)
	}
	// The field is named, so a stage that has to wire a reference knows which
	// one to write. What is NOT here is an instruction to write it.
	if !strings.Contains(ev, "full_content_path") {
		t.Errorf("the field is not named:\n%s", ev)
	}
	// It used to say "Reference it as ${step.N.full_content_path} in a later
	// step". Five stages read this text and only the planner can act on that;
	// the reflector followed it, filled the literal N with the node's tag, and
	// put a pointer in its summary where the value belonged.
	if strings.Contains(ev, "${step.") {
		t.Errorf("evidence carries a wiring instruction meant for the planner:\n%s", ev)
	}
}

func TestEvidence_NamesAKeptCommandOutput(t *testing.T) {
	b := bodyWith(t, toolapi.ToolOK("command", "the first few lines", map[string]any{
		"output_path":  "output/command_123.txt",
		"output_bytes": 900000,
	}))
	ev := b.Evidence()
	if !strings.Contains(ev, "output/command_123.txt") || !strings.Contains(ev, "output_path") {
		t.Errorf("a kept command output is not named:\n%s", ev)
	}
}

// A partial file has to say so, or a caller sent to it for the rest finds the
// same beginning again and believes it is the whole.
func TestEvidence_SaysWhenTheKeptFileIsPartial(t *testing.T) {
	b := bodyWith(t, toolapi.ToolOK("page", "content", map[string]any{
		"full_content_path": "fetched/example_123",
		"body_truncated":    true,
	}))
	if ev := b.Evidence(); !strings.Contains(ev, "not all of it") {
		t.Errorf("a partial file is presented as complete:\n%s", ev)
	}
}

// Every other tool's result is unchanged: a payload with no such field gets no
// extra line, so nothing grows a frame around it.
func TestEvidence_UnchangedWhenThereIsNoFile(t *testing.T) {
	for _, msg := range []toolapi.ToolMessage{
		toolapi.ToolOK("search", "results here", map[string]any{"query": "x"}),
		toolapi.ToolOK("net", "interfaces", nil),
		toolapi.ToolText("just prose"),
	} {
		ev := bodyWith(t, msg).Evidence()
		if strings.Contains(ev, "reference it as") {
			t.Errorf("a result with no kept file grew a line about one:\n%s", ev)
		}
	}
}
