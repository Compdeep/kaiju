package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// createdJSON is a compute result naming the files it wrote.
const createdJSON = `{"type":"result","files_created":["collector/main.py","collector/util.py"]}`

// Phase 4 — the typed Summary() finally drives the frontend trace header.
//
// This is the first phase a user sees. Every Summary() written in phases 1a, 1b
// and 2 was dormant until now.
//
// RawTextBody is deliberately excluded: it is the fallback every un-migrated
// producer still uses, and its Summary is only the first non-empty line, which
// is weaker than the nodeSummary heuristics it would displace. So the change is
// confined to nodes that actually carry a typed body.

// traceLine builds the node info the frontend receives.
func traceLine(t *testing.T, n *Node, set func(g *Graph, id string)) string {
	t.Helper()
	g := NewGraph()
	id := g.AddNode(n)
	set(g, id)
	info := g.SnapshotNode(id)
	if info == nil {
		t.Fatal("no node info")
	}
	return info.Summary
}

// TestTraceUsesTheReflectionDecision: a concluded reflection shows its decision
// and reason rather than the outcome prose it used to store.
func TestTraceUsesTheReflectionDecision(t *testing.T) {
	ref, err := parseReflectionOutput(concludeJSON)
	if err != nil {
		t.Fatalf("parseReflectionOutput: %v", err)
	}

	got := traceLine(t, &Node{Type: NodeReflection}, func(g *Graph, id string) {
		g.SetBody(id, ReflectionBody{Out: *ref, Raw: concludeJSON})
	})

	if !strings.HasPrefix(got, "conclude:") {
		t.Errorf("trace = %q, want it to start with \"conclude:\"", got)
	}
	if !strings.Contains(got, "credential dump") {
		t.Errorf("trace = %q, want the reflector's reason", got)
	}
}

// TestTraceUsesTheComputeDescriptor: compute shows what it did. Its plan
// carries none of the keys nodeSummary looks for (result, output, message,
// status, content, title, name), so before this the trace was the raw JSON.
func TestTraceUsesTheComputeDescriptor(t *testing.T) {
	got := traceLine(t, &Node{Type: NodeCompute, ToolName: "compute"}, func(g *Graph, id string) {
		g.SetBody(id, NewToolBody(computeMessage("compute", createdJSON)))
	})

	if !strings.Contains(got, "created 2 file(s): collector/main.py") {
		t.Errorf("trace = %q, want the compute descriptor", got)
	}

	// The old path really was worse — confirm the heuristics do not produce
	// this, or the test is claiming an improvement that did not happen.
	old := nodeSummary(&Node{Type: NodeCompute, Result: createdJSON})
	if old == got {
		t.Errorf("precondition failed: nodeSummary already produced %q", old)
	}
}

// TestTraceUsesToolStatus: a tool that emitted an envelope shows kind and
// status, so an empty result is visibly empty rather than looking like output.
func TestTraceUsesToolStatus(t *testing.T) {
	got := traceLine(t, &Node{Type: NodeTool, ToolName: "web_search"}, func(g *Graph, id string) {
		g.SetBody(id, toolMessageBody{msg: toolapi.ToolEmpty("search", "no results for that query")})
	})

	if !strings.HasPrefix(got, "search empty") {
		t.Errorf("trace = %q, want it to lead with \"search empty\"", got)
	}
	if !strings.Contains(got, "no results") {
		t.Errorf("trace = %q, want the reason", got)
	}
}

// TestTraceUnchangedForUnmigratedProducers is the no-regression claim, and the
// reason RawTextBody is excluded. Every tool in this repo takes this path.
func TestTraceUnchangedForUnmigratedProducers(t *testing.T) {
	cases := []struct {
		name   string
		node   *Node
		result string
	}{
		{"tool returning JSON with a message", &Node{Type: NodeTool, ToolName: "get_alerts"}, `{"message":"3 alerts found","count":3}`},
		{"tool returning prose", &Node{Type: NodeTool, ToolName: "get_alerts"}, "first line\nsecond line"},
		{"a verify node", &Node{Type: NodeTool, Tag: "verify_fix"}, "the fix holds"},
		{"an observer node", &Node{Type: NodeObserver}, `{"decision":"continue","reason":"looks fine"}`},
		{"an interjection node", &Node{Type: NodeInterjection}, `{"action":"steer","reason":"user asked"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// What the frontend gets now, via SetResult (which wraps in RawTextBody).
			got := traceLine(t, tc.node, func(g *Graph, id string) {
				g.SetResult(id, tc.result)
			})

			// What nodeSummary alone produces — the pre-phase-4 behaviour.
			probe := *tc.node
			probe.Result = tc.result
			want := nodeSummary(&probe)

			if got != want {
				t.Errorf("trace changed for an un-migrated producer:\n got  %q\n want %q", got, want)
			}
			// And it must not be RawTextBody's weaker first-line summary.
			if raw := RawText(tc.result).Summary(); got != want && got == raw {
				t.Errorf("trace fell back to RawTextBody.Summary() = %q", raw)
			}
		})
	}
}
