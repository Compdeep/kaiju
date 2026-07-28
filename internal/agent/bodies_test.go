package agent

import (
	"strings"
	"testing"
)

func TestReflectionBody(t *testing.T) {
	raw := `{"decision":"conclude","summary":"done","reason":"answered"}`
	b := ReflectionBody{Out: reflectionOutput{Decision: "conclude", Summary: "done", Reason: "answered"}, Raw: raw}
	if b.Evidence() != raw {
		t.Fatalf("ReflectionBody.Evidence should be the raw JSON so no field is lost")
	}
	if got := b.Summary(); !strings.HasPrefix(got, "conclude") || !strings.Contains(got, "answered") {
		t.Fatalf("ReflectionBody.Summary = %q want 'conclude: answered'", got)
	}
}

func TestComputeBody(t *testing.T) {
	bp := parseComputeBody(`{"type":"blueprint","project_root":"/p"}`)
	if bp.Type != "blueprint" || bp.Summary() != "blueprint: /p" {
		t.Fatalf("blueprint summary = %q", bp.Summary())
	}
	if v, ok := bp.Field("project_root"); !ok || v != "/p" {
		t.Fatalf("ComputeBody.Field(project_root) = %v,%v", v, ok)
	}
	if res := parseComputeBody(`{"type":"result","files_created":["a.py","b.py"]}`).Summary(); res != "created 2 file(s): a.py" {
		t.Fatalf("result summary = %q", res)
	}
	if noop := parseComputeBody(`{"type":"result","no_changes":true,"reason":"nothing to do"}`).Summary(); !strings.Contains(noop, "no changes") || !strings.Contains(noop, "nothing to do") {
		t.Fatalf("noop summary = %q", noop)
	}
}

func TestControlBodies(t *testing.T) {
	if got := parseMicroPlannerBody(`{"summary":"fix the import","nodes":[]}`).Summary(); !strings.Contains(got, "fix the import") {
		t.Fatalf("microplanner summary = %q", got)
	}
	if got := parseObserverBody(`{"action":"inject","reason":"need a step"}`).Summary(); !strings.HasPrefix(got, "inject") || !strings.Contains(got, "need a step") {
		t.Fatalf("observer summary = %q", got)
	}
	if got := parseHolmesBody(`{"reasoning":"looks like a nil deref","hypothesis":"x is nil"}`).Summary(); !strings.Contains(got, "x is nil") {
		t.Fatalf("holmes summary = %q", got)
	}
	// Holmes conclude → RCA root cause surfaces in the summary
	if got := parseHolmesBody(`{"conclude":true,"rca":{"root_cause":"missing import"}}`).Summary(); !strings.Contains(got, "missing import") {
		t.Fatalf("holmes conclude summary = %q want the RCA root cause", got)
	}
}
