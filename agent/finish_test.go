package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A step ends the same way wherever it ran.
//
// fireNode has two paths that both mean "run a step", and they were kept level
// with each other by hand. The remote one was found short four times: three
// gate checks, the envelope it did not parse, a target with no executor falling
// through to local execution, and the display hint — which the local path
// attached and it did not, so a panel never appeared for a step that ran
// somewhere else.
//
// Each was fixed where it was found, and nothing stopped the next one. These
// hold the shape rather than the individual fixes.

// Two kinds of display hint, because the difference between them is the one
// thing the local and remote paths are allowed to do differently.
//
// A hint naming a PATH is only meaningful where the file is: the panel opens it
// on the machine running the dashboard. A hint carrying inline CONTENT already
// holds what it shows and travels.

// panelTool names a path — what file_write does.
type panelTool struct{ runs int }

func (p *panelTool) Name() string              { return "counter" }
func (p *panelTool) Description() string       { return "returns a panel hint naming a path" }
func (p *panelTool) Impact(map[string]any) int { return toolapi.ImpactObserve }
func (p *panelTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (p *panelTool) Execute(context.Context, map[string]any) (string, error) {
	p.runs++
	return "ran", nil
}
func (p *panelTool) DisplayHint(map[string]any, string) *toolapi.DisplayHint {
	return &toolapi.DisplayHint{Plugin: "code", Title: "counter output", Path: "/tmp/x", Line: 3}
}

// inlineTool carries its content — what panel_push does.
type inlineTool struct{ panelTool }

func (i *inlineTool) DisplayHint(map[string]any, string) *toolapi.DisplayHint {
	return &toolapi.DisplayHint{Plugin: "markdown", Title: "counter output", Content: "# ran"}
}

// fireWith runs one node through the dispatcher with the given scope and
// returns its completion and the node, which the dispatcher writes to.
func fireWith(t *testing.T, a *Agent, n *Node) (nodeCompletion, *Node) {
	t.Helper()
	graph := NewGraph()
	id := graph.AddNode(n)
	ch := make(chan nodeCompletion, 1)
	a.fireNode(context.Background(), graph.Get(id), graph,
		NewBudget(20, 5, 20, 5, time.Minute), ch, "", newToolThrottle(), gates.Intent(0), nil)
	select {
	case c := <-ch:
		return c, graph.Get(id)
	case <-time.After(5 * time.Second):
		t.Fatal("fireNode produced no completion")
		return nodeCompletion{}, nil
	}
}

// A step that ran elsewhere carries the panel it can carry.
//
// Inline content travels: it holds what it shows. A path does not, and showing
// it would open this machine's file of that name as though it were the other
// machine's — which is worse than showing nothing, because it looks right.
func TestAPanelTravelsOnlyWhenItCarriesWhatItShows(t *testing.T) {
	cases := []struct {
		name string
		tool toolapi.Tool
		want int
	}{
		{"a path, on another machine", &panelTool{}, 0},
		{"inline content, on another machine", &inlineTool{}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := agentWith(t, c.tool)
			a.remoteExec = okExec{}

			got, n := fireWith(t, a, &Node{Type: NodeTool, ToolName: "counter", Target: "machine-b"})
			if got.Err != nil {
				t.Fatalf("the step failed: %v", got.Err)
			}
			if len(n.Actions) != c.want {
				t.Errorf("the step attached %d panels, want %d", len(n.Actions), c.want)
			}
		})
	}
}

// Here, both kinds show: the file is on this machine, so its path resolves.
func TestAStepThatRanHereCarriesEitherPanel(t *testing.T) {
	for _, tool := range []toolapi.Tool{&panelTool{}, &inlineTool{}} {
		_, n := fireWith(t, agentWith(t, tool), &Node{Type: NodeTool, ToolName: "counter"})
		if len(n.Actions) != 1 {
			t.Errorf("%T attached %d panels for a step that ran here, want 1", tool, len(n.Actions))
		}
	}
}

// A step that failed has no output to show, so no panel is attached for either
// path. Without this the check above would pass on an agent that attaches one
// unconditionally.
func TestAFailedStepCarriesNoPanel(t *testing.T) {
	a := agentWith(t, &panelTool{})
	a.remoteExec = failingExec{}

	c, n := fireWith(t, a, &Node{Type: NodeTool, ToolName: "counter", Target: "machine-b"})
	if c.Err == nil {
		t.Fatal("an unreachable machine reported success")
	}
	if len(n.Actions) != 0 {
		t.Errorf("a step that failed attached %d panels; there is no output to show", len(n.Actions))
	}
}

// Every refusal in fireNode is a completion too, and each has to name its node
// and carry its reason. They used to be written out one at a time, which is how
// one of them came to carry a result and no error.
func TestEveryRefusalCompletesTheNode(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*Agent)
		node  *Node
		// A refusal that the model should read as the call's answer rather
		// than as a failure of the run.
		asResult bool
	}{
		{
			name:  "no executor for another machine",
			setup: func(*Agent) {},
			node:  &Node{Type: NodeTool, ToolName: "counter", Target: "machine-b"},
		},
		{
			name: "a target the application rejects",
			setup: func(a *Agent) {
				a.remoteExec = okExec{}
				a.targetValid = func(string) error { return errors.New("no such machine") }
			},
			node: &Node{Type: NodeTool, ToolName: "counter", Target: "machine-b"},
		},
		{
			name: "the application's rule says no",
			setup: func(a *Agent) {
				a.remoteExec = okExec{}
				a.allowToolFn = func(context.Context, ToolCallRequest) (bool, string) {
					return false, "not on that machine"
				}
			},
			node:     &Node{Type: NodeTool, ToolName: "counter", Target: "machine-b"},
			asResult: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := agentWith(t, &panelTool{})
			c.setup(a)

			got, node := fireWith(t, a, c.node)

			if got.NodeID != node.ID {
				t.Errorf("the completion names %q and the node is %q", got.NodeID, node.ID)
			}
			if c.asResult {
				if got.Err != nil {
					t.Errorf("a refusal the model should read came back as an error: %v", got.Err)
				}
				if got.Result == "" {
					t.Error("the refusal carries no reason, so the model retries the same call")
				}
				return
			}
			if got.Err == nil {
				t.Fatal("the step was refused and the completion says nothing went wrong")
			}
			if !strings.Contains(got.Err.Error(), "machine-b") {
				t.Errorf("err = %v; the refusal does not name the machine", got.Err)
			}
		})
	}
}
