package agent

import (
	"context"
	"strings"
	"testing"
)

func TestExecContextFrom_AbsentIsNilNotAPanic(t *testing.T) {
	if ec := ExecContextFrom(context.Background()); ec != nil {
		t.Fatalf("want nil for a ctx that carries none, got %+v", ec)
	}
}

func TestWithExecContext_RoundTrips(t *testing.T) {
	g := NewGraph()
	want := &ExecuteContext{Graph: g, Workspace: "/tmp/ws", AlertID: "a-1"}

	ctx := WithExecContext(context.Background(), want)
	got := ExecContextFrom(ctx)

	if got == nil {
		t.Fatal("nothing came back")
	}
	if got.Graph != g || got.Workspace != "/tmp/ws" || got.AlertID != "a-1" {
		t.Fatalf("got %+v, want the state that went in", got)
	}
}

// A nil state must not replace one already on the ctx with something a tool
// would dereference.
func TestWithExecContext_NilIsANoOp(t *testing.T) {
	ctx := WithExecContext(context.Background(), &ExecuteContext{AlertID: "a-1"})
	ctx = WithExecContext(ctx, nil)
	if ec := ExecContextFrom(ctx); ec == nil || ec.AlertID != "a-1" {
		t.Fatalf("a nil must leave the existing state alone, got %+v", ec)
	}
}

// The guard for the fault this exists to remove: a tool that returns a typed
// message takes the typed branch of the dispatcher, which used to mean its
// ExecuteContext was never built. It must now be reachable from the ctx that
// ExecuteTyped receives — the same ctx, on either branch.
func TestDispatcherBuildsTheStateBeforeChoosingAPath(t *testing.T) {
	src := readSource(t, "dispatcher.go")

	build := indexOf(src, "ctx = WithExecContext(ctx, ec)")
	fork := indexOf(src, "if tx, ok := skill.(tools.TypedExecutor); ok {")
	if build < 0 || fork < 0 {
		t.Fatal("the dispatcher no longer builds the state, or no longer forks on the typed interface")
	}
	if build > fork {
		t.Fatal("the run state is built after the typed/contextual fork — a typed tool cannot reach it")
	}

	// And the contextual branch must reuse that same state rather than build a
	// second one, or the two paths drift.
	if strings.Count(src, "ec = &ExecuteContext{") != 1 {
		t.Errorf("ExecuteContext should be built exactly once per tool call, found %d sites",
			strings.Count(src, "ec = &ExecuteContext{"))
	}
}
