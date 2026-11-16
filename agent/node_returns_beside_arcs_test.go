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

// callText is everything one reflector-shaped call is sent: the tool messages
// and "### Resolved" beside them.
func callText(g *Graph) string {
	user := "## Node Results\n\n" + (func() string {
		text, _ := (&nodeReturnsSource{}).Load(g, &Trigger{}, &Agent{}, NodeReturnsBesideArcs().Params)
		return text
	})()
	var all strings.Builder
	for _, m := range BuildMessagesWithResults("system", user, nil, g.Arcs()) {
		all.WriteString(m.Content)
		all.WriteString("\n")
	}
	return all.String()
}

// Text that is not among the fields: the tool message carries it beside the
// fields, so it reaches the call once, there. It used to be left out of the
// tool message, and a stage that reads only tool messages (the replan) never
// saw a fetched page.
func TestBesideArcsKeepsTextTheFieldsDoNotHold(t *testing.T) {
	g := besideArcsGraph(t, map[string]toolapi.ToolMessage{
		"stop_nginx": toolapi.ToolOK("service", "stopped nginx", map[string]any{"name": "nginx", "was_running": true}),
	})
	var tool string
	for _, r := range g.StepResults(-1) {
		tool += stepResultContent(r)
	}
	if !strings.Contains(tool, "stopped nginx") || !strings.Contains(tool, `"was_running":true`) {
		t.Errorf("the tool message should carry the text beside the fields: %s", tool)
	}
	if n := strings.Count(callText(g), "stopped nginx"); n != 1 {
		t.Errorf("the text reaches the call %d times, want 1", n)
	}
}

// A field cut on its way into the tool message: the text beside the fields is
// whole, so the end of the output still reaches the call.
func TestBesideArcsKeepsTextWhoseFieldWasCut(t *testing.T) {
	out := strings.Repeat("x", payloadValueChars+500) + "END_OF_OUTPUT"
	g := besideArcsGraph(t, map[string]toolapi.ToolMessage{
		"long_command": toolapi.ToolOK("command", out, map[string]any{"exit_code": 0, "stdout": out, "command": "cat big"}),
	})
	if !strings.Contains(callText(g), out) {
		t.Errorf("the whole output does not reach the call")
	}
}

// Text longer than the evidence budget is cut in the tool message, and so is
// written again under "### Resolved", where the stage's own budget applies.
func TestBesideArcsKeepsTextCutInTheToolMessage(t *testing.T) {
	out := "START " + strings.Repeat("y", toolapi.EvidenceBudget+2000) + " END_OF_PAGE"
	g := besideArcsGraph(t, map[string]toolapi.ToolMessage{
		"fetch_page": toolapi.ToolOK("page", out, map[string]any{"status": "HTTP 200", "url": "https://example.org"}),
	})
	if beside := loadReturns(t, g, NodeReturnsBesideArcs()); strings.Contains(beside, inToolMessage) {
		t.Errorf("a step whose text was cut in its tool message is named as held there:\n%.300s", beside)
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
