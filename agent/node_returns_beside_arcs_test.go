package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A step's output written twice into one call.
//
// The reflector, the reframe edges and the aggregator are given every step as
// a tool message (graph.Arcs) and again under "### Resolved" (node_returns).
// Where the two hold the same text, the second is the same tokens again: on
// run 19:40 2026-11-12, "LISTEN" was 31 times in the tool messages and 31
// times in the user prompt of one reflector call, and "### Resolved" was 29%
// of the run's input. A call that has the tool messages asks for node_returns
// beside them, and a step whose tool message already holds its output whole
// is named there without it.

func besideArcsGraph(t *testing.T, steps map[string]toolapi.ToolMessage) *Graph {
	t.Helper()
	g := NewGraph()
	for tag, msg := range steps {
		id := g.AddNode(&Node{Type: NodeTool, Tag: tag, ToolName: tag})
		g.SetBody(id, toolMessageBody{msg: msg})
	}
	return g
}

func loadReturns(t *testing.T, g *Graph, spec SourceSpec) string {
	t.Helper()
	text, err := (&nodeReturnsSource{}).Load(g, &Trigger{}, &Agent{}, spec.Params)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// Fields and no text: what "### Resolved" would show is the fields, which the
// tool message carries.
func TestBesideArcsLeavesOutAStepWithFieldsOnly(t *testing.T) {
	g := besideArcsGraph(t, map[string]toolapi.ToolMessage{
		"list_procs": toolapi.ToolOK("processes", "", map[string]any{
			"count": 1, "processes": []any{map[string]any{"command": "tor --defaults-torrc"}},
		}),
	})
	beside := loadReturns(t, g, NodeReturnsBesideArcs())
	if strings.Contains(beside, "tor --defaults-torrc") {
		t.Errorf("the output is written again beside the tool message:\n%s", beside)
	}
	if !strings.Contains(beside, "list_procs") || !strings.Contains(beside, inToolMessage) {
		t.Errorf("the step should still be named, with where its output is:\n%s", beside)
	}
	if whole := loadReturns(t, g, NodeReturns("all")); !strings.Contains(whole, "tor --defaults-torrc") {
		t.Errorf("a call without tool messages lost the output:\n%s", whole)
	}
}

// A command whose text is exactly its stdout field.
func TestBesideArcsLeavesOutACommandWhoseTextIsItsStdout(t *testing.T) {
	out := "LISTEN 0 4096 0.0.0.0:22\n"
	g := besideArcsGraph(t, map[string]toolapi.ToolMessage{
		"listen_ports": toolapi.ToolOK("command", out, map[string]any{
			"exit_code": 0, "stdout": out, "stderr": "", "command": "ss -tlnp",
			"output_path": "output/command_1.txt",
		}),
	})
	if beside := loadReturns(t, g, NodeReturnsBesideArcs()); strings.Contains(beside, "LISTEN") {
		t.Errorf("the command's output is written again beside the tool message:\n%s", beside)
	}
}

// Text that is not among the fields exists only in "### Resolved": it stays.
func TestBesideArcsKeepsTextTheFieldsDoNotHold(t *testing.T) {
	g := besideArcsGraph(t, map[string]toolapi.ToolMessage{
		"stop_nginx": toolapi.ToolOK("service", "stopped nginx", map[string]any{"name": "nginx", "was_running": true}),
	})
	if beside := loadReturns(t, g, NodeReturnsBesideArcs()); !strings.Contains(beside, "stopped nginx") {
		t.Errorf("text found nowhere else was left out:\n%s", beside)
	}
}

// A field cut on its way into the tool message: the tool message holds part of
// the text, so the whole of it stays.
func TestBesideArcsKeepsTextWhoseFieldWasCut(t *testing.T) {
	out := strings.Repeat("x", payloadValueChars+500) + "END_OF_OUTPUT"
	g := besideArcsGraph(t, map[string]toolapi.ToolMessage{
		"long_command": toolapi.ToolOK("command", out, map[string]any{"exit_code": 0, "stdout": out, "command": "cat big"}),
	})
	if beside := loadReturns(t, g, NodeReturnsBesideArcs()); !strings.Contains(beside, "END_OF_OUTPUT") {
		t.Errorf("text the tool message holds only part of was left out:\n%.300s", beside)
	}
}

// Failures are reported as they were.
func TestBesideArcsKeepsFailures(t *testing.T) {
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "read_sudoers", ToolName: "bash"})
	g.SetError(id, errString("cat: /etc/sudoers: Permission denied"))
	if beside := loadReturns(t, g, NodeReturnsBesideArcs()); !strings.Contains(beside, "Permission denied") {
		t.Errorf("the failure is missing:\n%s", beside)
	}
}

// What a reflector call is sent: each output once.
func TestEachOutputReachesTheCallOnce(t *testing.T) {
	g := besideArcsGraph(t, map[string]toolapi.ToolMessage{
		"list_procs": toolapi.ToolOK("processes", "", map[string]any{
			"count": 1, "processes": []any{map[string]any{"command": "tor --defaults-torrc"}},
		}),
		"stop_nginx": toolapi.ToolOK("service", "stopped nginx", map[string]any{"name": "nginx", "was_running": true}),
	})
	user := "## Node Results\n\n" + loadReturns(t, g, NodeReturnsBesideArcs())
	var all strings.Builder
	for _, m := range BuildMessagesWithResults("system", user, nil, g.Arcs()) {
		all.WriteString(m.Content)
		all.WriteString("\n")
	}
	if n := strings.Count(all.String(), "tor --defaults-torrc"); n != 1 {
		t.Errorf("the process list reaches the call %d times, want 1", n)
	}
	if n := strings.Count(all.String(), "stopped nginx"); n != 1 {
		t.Errorf("the service text reaches the call %d times, want 1", n)
	}
}
