package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Running the dispatcher, rather than a copy of what it does.
//
// Every other test of this path reproduces its logic — simulateDispatch in
// intent_enforcement_test.go says so in its own comment — which cannot notice
// the real one changing. This calls executeToolNode.

// spyTool records what the dispatcher handed it.
type spyTool struct {
	sawRunState  bool
	sawWorkspace string
}

func (s *spyTool) Name() string              { return "spy" }
func (s *spyTool) Description() string       { return "records what it was given" }
func (s *spyTool) Impact(map[string]any) int { return toolapi.ImpactObserve }
func (s *spyTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (s *spyTool) Execute(ctx context.Context, p map[string]any) (string, error) {
	return toolapi.StringResult(s.ExecuteTyped(ctx, p))
}
func (s *spyTool) ExecuteTyped(ctx context.Context, _ map[string]any) (toolapi.ToolMessage, error) {
	if ec := ExecContextFrom(ctx); ec != nil {
		s.sawRunState = true
		s.sawWorkspace = ec.Workspace
	}
	return toolapi.ToolOK("spy", "ran", map[string]any{"ok": true}), nil
}

// A typed tool is called with the run state on its ctx, and its envelope comes
// back as a typed body rather than a string the caller has to re-parse.
func TestTheDispatcherGivesATypedToolTheRunState(t *testing.T) {
	reg, gate, _ := newTestStack(t)

	spy := &spyTool{}
	registry := toolapi.NewRegistry()
	if err := registry.Register(spy); err != nil {
		t.Fatalf("register: %v", err)
	}

	a := &Agent{registry: registry, gate: gate, intentRegistry: reg}
	a.cfg.Workspace = "/tmp/spy-workspace"

	graph := NewGraph()
	id := graph.AddNode(&Node{Type: NodeTool, ToolName: "spy"})

	result, body, err := a.executeToolNode(context.Background(), graph.Get(id), graph,
		NewBudget(20, 5, 20, 5, time.Minute), "spy", map[string]any{}, "", gates.Intent(0), nil)
	if err != nil {
		t.Fatalf("executeToolNode: %v", err)
	}

	if !spy.sawRunState {
		t.Error("the tool was called without the run state — a typed tool that needs the " +
			"graph, budget or model would fail here with nothing at build time to warn anyone")
	}
	if spy.sawWorkspace != "/tmp/spy-workspace" {
		t.Errorf("workspace = %q, want the one the agent is configured with", spy.sawWorkspace)
	}

	tb, ok := body.(toolMessageBody)
	if !ok {
		t.Fatalf("body is %T, want the tool's envelope", body)
	}
	if tb.Envelope().Status != toolapi.StatusOK || tb.Envelope().Type != "spy" {
		t.Errorf("envelope = %+v, want the tool's own", tb.Envelope())
	}
	if msg, ok := toolapi.ParseToolMessage(result); !ok || msg.Content != "ran" {
		t.Errorf("result = %q, want the envelope's JSON", result)
	}
}
