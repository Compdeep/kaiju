package agent

import (
	"context"
	"testing"
)

// Each stage of a run gets its own cache key; the two reframe edges, which have
// different system prompts, get two. A planner retry shares the planner's.
func TestStageCacheKey(t *testing.T) {
	run := withRunID(context.Background(), "r1")
	for _, c := range []struct {
		id   TraceID
		want string
	}{
		{TraceID{NodeType: "reflector", Tag: "reflect"}, "r1:reflector"},
		{TraceID{NodeType: "reframe", Tag: "reframe:reflect"}, "r1:reframe:reflect"},
		{TraceID{NodeType: "reframe", Tag: "reframe:plan"}, "r1:reframe:plan"},
		{TraceID{NodeType: "executive_native", Tag: "plan"}, "r1:executive_native"},
		{TraceID{NodeType: "executive_native", Tag: "plan_shorter"}, "r1:executive_native"},
		{TraceID{}, "r1"},
	} {
		if got := stageCacheKey(withTrace(run, c.id)); got != c.want {
			t.Errorf("%+v: key %q, want %q", c.id, got, c.want)
		}
	}
	if got := stageCacheKey(run); got != "r1" {
		t.Errorf("no trace: key %q, want the run id", got)
	}
	if got := stageCacheKey(context.Background()); got != "" {
		t.Errorf("no run: key %q, want none", got)
	}
}
