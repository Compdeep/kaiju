package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// ExecuteTyped — a tool hands back a ToolMessage instead of a marshalled
// string, so the dispatcher puts the typed body straight on the node with no
// marshal-then-reparse round trip.
//
// Inert until a tool opts in: nothing in this repo implements ExecuteTyped, and
// a tool that does not is untouched — its body is nil and it flows through the
// path it always did.
//
// These go through executeToolNode itself rather than asserting on source, so
// they exercise the dispatch decision rather than restating it.

// typedStub implements TypedExecutor, returning an envelope directly.
type typedStub struct {
	msg    toolapi.ToolMessage
	err    error
	called bool
}

func (s *typedStub) Name() string              { return "typed_stub" }
func (*typedStub) Description() string         { return "typed stub for tests" }
func (*typedStub) RequiresTarget() bool        { return false }
func (*typedStub) Impact(map[string]any) int   { return 0 }
func (*typedStub) Parameters() json.RawMessage { return json.RawMessage(`{}`) }

// Execute must never be reached when ExecuteTyped exists.
func (*typedStub) Execute(_ context.Context, _ map[string]any) (string, error) {
	return "PLAIN EXECUTE WAS CALLED", nil
}

func (s *typedStub) ExecuteTyped(_ context.Context, _ map[string]any) (toolapi.ToolMessage, error) {
	s.called = true
	return s.msg, s.err
}

// plainStub returns a bare string, like every tool in this repo today.
type plainStub struct{ out string }

func (*plainStub) Name() string                { return "plain_stub" }
func (*plainStub) Description() string         { return "plain stub for tests" }
func (*plainStub) RequiresTarget() bool        { return false }
func (*plainStub) Impact(map[string]any) int   { return 0 }
func (*plainStub) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (s *plainStub) Execute(_ context.Context, _ map[string]any) (string, error) {
	return s.out, nil
}

// agentWithTool builds a real agent through New and registers one tool.
func agentWithTool(t *testing.T, tool toolapi.Tool) *Agent {
	t.Helper()
	d := t.TempDir()
	a, err := New(Config{PathConfig: PathConfig{Workspace: d, DataDir: d, MetadataDir: d}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := a.registry.Register(tool); err != nil {
		t.Fatalf("Register %s: %v", tool.Name(), err)
	}
	return a
}

// TestExecuteTypedReturnsTheBody: a typed tool's envelope reaches the caller
// without a round trip, and Result carries the envelope JSON for the frontend.
func TestExecuteTypedReturnsTheBody(t *testing.T) {
	stub := &typedStub{msg: toolapi.ToolOK("listing", "2 processes",
		map[string]any{"processes": []any{"sshd", "nginx"}})}
	a := agentWithTool(t, stub)

	result, body, err := a.executeToolNode(context.Background(), nil, nil, nil,
		"typed_stub", map[string]any{}, "alert-1", gates.Intent(0), nil)
	if err != nil {
		t.Fatalf("executeToolNode: %v", err)
	}
	if !stub.called {
		t.Fatal("ExecuteTyped was not called")
	}
	if result == "PLAIN EXECUTE WAS CALLED" {
		t.Fatal("plain Execute ran even though the tool implements ExecuteTyped")
	}

	if body == nil {
		t.Fatal("body is nil — the typed path did not produce one")
	}
	tmb, ok := body.(toolMessageBody)
	if !ok {
		t.Fatalf("body is %T, want toolMessageBody", body)
	}
	if tmb.Envelope().Status != toolapi.StatusOK {
		t.Errorf("Status = %v, want ok", tmb.Envelope().Status)
	}
	if v, ok := body.Field("processes.0"); !ok || v != "sshd" {
		t.Errorf(`Field("processes.0") = %v, %v`, v, ok)
	}

	// Result is the envelope JSON, so the frontend and persistence contract is
	// unchanged and the envelope survives a restart.
	if _, ok := toolapi.ParseToolMessage(result); !ok {
		t.Errorf("Result %q does not parse as an envelope", result)
	}
}

// TestExecuteTypedPropagatesFailure: an error from the typed path is returned,
// not swallowed into a body.
func TestExecuteTypedPropagatesFailure(t *testing.T) {
	sentinel := errors.New("the tool failed")
	a := agentWithTool(t, &typedStub{err: sentinel})

	_, body, err := a.executeToolNode(context.Background(), nil, nil, nil,
		"typed_stub", map[string]any{}, "alert-1", gates.Intent(0), nil)

	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the tool's error", err)
	}
	if body != nil {
		t.Errorf("body = %v, want nil when the tool failed", body)
	}
}

// TestPlainToolIsUnchanged is the compatibility claim: a tool that has not opted
// in gets a nil body, so the scheduler handles it exactly as before.
func TestPlainToolIsUnchanged(t *testing.T) {
	a := agentWithTool(t, &plainStub{out: `{"text":"3 alerts","count":3}`})

	result, body, err := a.executeToolNode(context.Background(), nil, nil, nil,
		"plain_stub", map[string]any{}, "alert-1", gates.Intent(0), nil)
	if err != nil {
		t.Fatalf("executeToolNode: %v", err)
	}
	if body != nil {
		t.Errorf("body = %v, want nil for a tool that returns a plain string", body)
	}
	if result != `{"text":"3 alerts","count":3}` {
		t.Errorf("result = %q, want the tool's output verbatim", result)
	}
}

// TestSchedulerBranchOrder pins the scheduler's CALL SITE and its order.
//
// The typed branch must come first: a completion carrying a body must not fall
// through to re-deriving one from the string. The scheduler branch sits inside
// runPlanAndSchedule and cannot be called on its own, so this asserts the
// source — the test below reproduces the branch and therefore cannot notice the
// real one changing.
func TestSchedulerBranchOrder(t *testing.T) {
	src, err := os.ReadFile("scheduler.go")
	if err != nil {
		t.Fatalf("read scheduler.go: %v", err)
	}
	text := string(src)

	typed := strings.Index(text, "if comp.Body != nil {")
	envelope := strings.Index(text, "} else if msg, ok := toolapi.ParseToolMessage(comp.Result); ok {")

	if typed < 0 {
		t.Fatal("the scheduler no longer prefers a completion's typed body")
	}
	if envelope < 0 {
		t.Fatal("the envelope branch is missing")
	}
	if typed > envelope {
		t.Errorf("branch order is wrong: typed=%d envelope=%d; "+
			"a typed body must be preferred over re-deriving one", typed, envelope)
	}
	// compute used to have a branch of its own here, re-deriving a body the
	// dispatcher had not built. It returns a ToolMessage now, so it takes the
	// first branch like every other tool.
	if strings.Contains(text, "} else if node.Type == NodeCompute {") {
		t.Error("compute has a body-deriving branch again — it is typed and does not need one")
	}
}

// TestTypedBodyReachesTheNode: a completion carrying a body ends with that body
// on the node, and absence renders as an explicit line rather than "".
func TestTypedBodyReachesTheNode(t *testing.T) {
	msg := toolapi.ToolEmpty("search", "no results")
	comp := nodeCompletion{NodeID: "n1", Result: msg.JSON(), Body: toolMessageBody{msg: msg}}

	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, ToolName: "typed_stub"})
	comp.NodeID = id

	// The branch the scheduler takes, in its order.
	switch {
	case comp.Body != nil:
		g.SetBody(comp.NodeID, comp.Body)
	default:
		t.Fatal("a completion carrying a body did not take the typed branch")
	}

	n := g.Get(id)
	if _, ok := n.Body.(toolMessageBody); !ok {
		t.Fatalf("node body is %T, want the completion's toolMessageBody", n.Body)
	}
	// Absence renders as an explicit line, not an empty string.
	if n.Result != "(no search: no results)" {
		t.Errorf("Result = %q, want the explicit absence line", n.Result)
	}
}
