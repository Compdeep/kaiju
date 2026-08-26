package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// resolvedEnvelopeNode is resolvedToolNode for a step that carries a real
// ToolMessage, which is what the outcome line is read off.
func resolvedEnvelopeNode(g *Graph, tag string, msg toolapi.ToolMessage) string {
	id := g.AddNode(&Node{Type: NodeTool, Tag: tag, ToolName: "bash"})
	g.SetState(id, StateResolved)
	g.SetBody(id, toolMessageBody{msg: msg})
	return id
}

// The run this comes from: a clone that finished was described as "produced a
// result" with no exit status, while the failure beside it carried exit 128 —
// and both open with the same line, because git prints it before it knows. The
// reader could not tell them apart and ruled against the run.
func TestStepOutcomes_SuccessIsDistinguishableFromFailure(t *testing.T) {
	const opening = "Cloning into '/tmp/xyz'...\n"

	g := NewGraph()
	resolvedEnvelopeNode(g, "clone_ok", toolapi.ToolOK("command", opening, map[string]any{
		"exit_code": 0,
		"command":   "git clone https://github.com/kelseyhightower/nocode.git /tmp/xyz",
	}))
	resolvedEnvelopeNode(g, "clone_dead", toolapi.ToolFail("command", "exit 128: exit status 128", map[string]any{
		"exit_code": 128,
		"stderr":    opening + "remote: Repository not found.\n",
	}))

	out, err := (&stepOutcomesSource{}).Load(g, nil, nil, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	okLine, deadLine := lineFor(t, out, "clone_ok"), lineFor(t, out, "clone_dead")
	if okLine == deadLine {
		t.Fatalf("a clone that worked and one that died read identically:\n%s", out)
	}
	if !strings.Contains(okLine, "succeeded") {
		t.Errorf("success is not called one: %q", okLine)
	}
	if strings.Contains(okLine, "produced a result") {
		t.Errorf("success still shares the unknown-body wording: %q", okLine)
	}
	if !strings.Contains(okLine, "exit 0") {
		t.Errorf("the decisive fact is missing from the success: %q", okLine)
	}
	if !strings.Contains(deadLine, "could not run") {
		t.Errorf("failure is not called one: %q", deadLine)
	}
}

// The exit status is stated once. An error's detail already carries the number,
// and repeating it reads as two separate problems.
func TestStepOutcomes_ExitStatusIsNotSaidTwice(t *testing.T) {
	g := NewGraph()
	resolvedEnvelopeNode(g, "boom", toolapi.ToolFail("command", "exit 128: exit status 128",
		map[string]any{"exit_code": 128}))

	out, err := (&stepOutcomesSource{}).Load(g, nil, nil, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if n := strings.Count(lineFor(t, out, "boom"), "128"); n != 2 {
		// "exit 128: exit status 128" is the detail's own wording: two mentions,
		// not three. A third means the code appended its own.
		t.Errorf("exit status appears %d times, want 2:\n%s", n, out)
	}
}

// A tool with no exit status of its own is unchanged by any of this.
func TestStepOutcomes_NoExitCodeStaysPlain(t *testing.T) {
	g := NewGraph()
	resolvedEnvelopeNode(g, "search", toolapi.ToolOK("search", "found 3", map[string]any{"results": []any{}}))

	out, err := (&stepOutcomesSource{}).Load(g, nil, nil, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	line := lineFor(t, out, "search")
	if !strings.Contains(line, "succeeded") {
		t.Errorf("success is not called one: %q", line)
	}
	if strings.Contains(line, "exit") {
		t.Errorf("an exit status was invented for a tool that has none: %q", line)
	}
}

func lineFor(t *testing.T, out, tag string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, tag) {
			return l
		}
	}
	t.Fatalf("no line for %q in:\n%s", tag, out)
	return ""
}
