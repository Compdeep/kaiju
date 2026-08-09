package agent

import (
	agenttools "github.com/Compdeep/kaiju/agent/tools"
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

// compute's outcome is read off the JSON it already returns — no second model
// call, no guess. A run that changed nothing is empty, because the coverage
// edge exists to tell an answering stage which steps produced nothing.
func TestComputeMessage(t *testing.T) {
	bp := computeMessage("compute", `{"type":"blueprint","project_root":"/p"}`)
	if bp.Status != agenttools.StatusOK || bp.Detail != "blueprint: /p" {
		t.Fatalf("blueprint = %q %q", bp.Status, bp.Detail)
	}
	if v, ok := NewToolBody(bp).Field("project_root"); !ok || v != "/p" {
		t.Fatalf("Field(project_root) = %v,%v — the plan has to stay addressable", v, ok)
	}
	if res := computeMessage("compute", `{"type":"result","files_created":["a.py","b.py"]}`); res.Detail != "created 2 file(s): a.py" {
		t.Fatalf("result detail = %q", res.Detail)
	}
	noop := computeMessage("compute", `{"type":"result","no_changes":true,"reason":"nothing to do"}`)
	if noop.Status != agenttools.StatusEmpty || !strings.Contains(noop.Detail, "nothing to do") {
		t.Fatalf("a run that changed nothing = %q %q, want empty carrying the reason", noop.Status, noop.Detail)
	}
	// Output that is not JSON still reaches the run rather than being called a
	// failure the tool never reported.
	if raw := computeMessage("compute", "the model wrote prose"); raw.Status != agenttools.StatusUnclassified {
		t.Fatalf("non-JSON = %q, want unclassified", raw.Status)
	}
}

// The plan goes in the payload and three grafts read it back out, so the two
// halves of that have to agree.
func TestComputePayloadRoundTrip(t *testing.T) {
	raw := `{"type":"blueprint","project_root":"/p"}`
	env := computeMessage("compute", raw).JSON()
	if got := computePayload(env); got != raw {
		t.Fatalf("computePayload = %q, want the plan back", got)
	}
	updated := `{"type":"blueprint","output":"ran"}`
	back := withComputePayload(env, updated)
	if got := computePayload(back); got != updated {
		t.Fatalf("after withComputePayload = %q, want the new plan", got)
	}
	// A bare result — a producer that built no envelope — passes through.
	if got := computePayload(raw); got != raw {
		t.Fatalf("a bare result should pass through, got %q", got)
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
