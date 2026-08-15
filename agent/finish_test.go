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

// panelTool returns a display hint, which is the thing the remote path used to
// drop on the floor.
type panelTool struct{ runs int }

func (p *panelTool) Name() string              { return "counter" }
func (p *panelTool) Description() string       { return "returns a panel hint" }
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

// The local path attached the tool's display hint and the remote path did not,
// so the frontend showed a panel for a file read here and nothing for the same
// read on another machine.
func TestAStepThatRanElsewhereStillCarriesItsPanel(t *testing.T) {
	tool := &panelTool{}

	local, localNode := fireWith(t, agentWith(t, tool), &Node{Type: NodeTool, ToolName: "counter"})
	if local.Err != nil {
		t.Fatalf("the local step failed: %v", local.Err)
	}
	if len(localNode.Actions) != 1 {
		t.Fatalf("the local step attached %d actions, want 1 — the test's premise is wrong", len(localNode.Actions))
	}

	remoteAgent := agentWith(t, tool)
	remoteAgent.remoteExec = okExec{}
	remote, remoteNode := fireWith(t, remoteAgent, &Node{Type: NodeTool, ToolName: "counter", Target: "machine-b"})
	if remote.Err != nil {
		t.Fatalf("the remote step failed: %v", remote.Err)
	}

	if len(remoteNode.Actions) != len(localNode.Actions) {
		t.Fatalf("a step that ran on machine-b carries %d actions and the same step here carries %d",
			len(remoteNode.Actions), len(localNode.Actions))
	}
	if remoteNode.Actions[0].Title != localNode.Actions[0].Title {
		t.Errorf("the panels differ: remote %q, local %q",
			remoteNode.Actions[0].Title, localNode.Actions[0].Title)
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
