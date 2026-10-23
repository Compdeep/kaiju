package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// targetStub acts on its target, at the impact it is given.
type targetStub struct {
	impact int
	ran    bool
}

func (*targetStub) Name() string                { return "target_stub" }
func (*targetStub) Description() string         { return "acts on its target" }
func (*targetStub) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (m *targetStub) Impact(map[string]any) int { return m.impact }
func (m *targetStub) Execute(context.Context, map[string]any) (string, error) {
	m.ran = true
	return "ran here", nil
}

type privilegedStub struct{ got *RemoteRequest }

func (p *privilegedStub) Execute(_ context.Context, req RemoteRequest) (string, error) {
	p.got = &req
	return toolapi.ToolOK("done", "ran as root", nil).JSON(), nil
}

// A step that acts on its target here at a level that allows root goes to the
// privileged program, as decided; below root it runs here.
func TestARootStepHereGoesToThePrivilegedProgram(t *testing.T) {
	tool := &targetStub{impact: toolapi.ImpactControl}
	a := agentWithTool(t, tool)
	a.gate = newTestGate(t, toolapi.ImpactControl)
	p := &privilegedStub{}
	a.privileged = p
	a.cfg.NodeID = "self"

	result, body, err := a.executeToolNode(context.Background(), nil, nil, nil,
		"target_stub", map[string]any{"x": "y"}, "run-1", gates.Intent(toolapi.ImpactControl), nil)
	if err != nil {
		t.Fatal(err)
	}
	if tool.ran || p.got == nil {
		t.Fatalf("ran here %v, handed over %v: %q", tool.ran, p.got != nil, result)
	}
	if p.got.Target != "self" || p.got.Tool != "target_stub" || p.got.Params["x"] != "y" || p.got.Intent != toolapi.ImpactControl {
		t.Fatalf("handed over as %+v", *p.got)
	}
	if body == nil {
		t.Fatal("the privileged program's envelope was not read")
	}

	below := &targetStub{impact: toolapi.ImpactAffect}
	a = agentWithTool(t, below)
	a.gate = newTestGate(t, toolapi.ImpactControl)
	p = &privilegedStub{}
	a.privileged = p
	if _, _, err := a.executeToolNode(context.Background(), nil, nil, nil,
		"target_stub", map[string]any{}, "run-2", gates.Intent(toolapi.ImpactAffect), nil); err != nil {
		t.Fatal(err)
	}
	if !below.ran || p.got != nil {
		t.Fatal("a step below root was handed to the privileged program")
	}
}
