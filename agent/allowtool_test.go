package agent

import (
	"context"
	"strings"
	"testing"
)

// An application refuses a call the engine allowed, and the model is told why.
func TestARefusalReachesTheModelAsAReason(t *testing.T) {
	a := &Agent{allowToolFn: func(_ context.Context, req ToolCallRequest) (bool, string) {
		if req.Tool == "create_incident" {
			return false, "incidents open from the finished outcome, not this tool"
		}
		return true, ""
	}}

	allow, reason := a.allowTool(context.Background(), ToolCallRequest{Tool: "create_incident"})
	if allow {
		t.Fatal("the call was allowed")
	}
	if !strings.Contains(reason, "finished outcome") {
		t.Errorf("reason = %q, want the application's own words", reason)
	}

	if allow, _ := a.allowTool(context.Background(), ToolCallRequest{Tool: "read_file"}); !allow {
		t.Error("a call the application had no rule about was refused")
	}
}

// No capability is the ordinary case: everything the engine allowed proceeds,
// exactly as before this existed.
func TestNoRuleAllowsEverything(t *testing.T) {
	if allow, reason := (&Agent{}).allowTool(context.Background(), ToolCallRequest{Tool: "bash"}); !allow || reason != "" {
		t.Errorf("allowTool = %v, %q; want allowed and silent", allow, reason)
	}
	var nilAgent *Agent
	if allow, _ := nilAgent.allowTool(context.Background(), ToolCallRequest{}); !allow {
		t.Error("a nil agent refused a call")
	}
}

// A refusal with nothing to say leaves the model to guess, and it guesses the
// same call again. The engine supplies words rather than returning an empty
// result that reads as success.
func TestASilentRefusalStillSaysSomething(t *testing.T) {
	a := &Agent{allowToolFn: func(context.Context, ToolCallRequest) (bool, string) { return false, "" }}

	allow, reason := a.allowTool(context.Background(), ToolCallRequest{Tool: "create_incident"})

	if allow {
		t.Fatal("the call was allowed")
	}
	if !strings.Contains(reason, "create_incident") {
		t.Errorf("reason = %q, want something naming the tool", reason)
	}
}

// The application may fill in a parameter the model left out, by writing to the
// map the call will run with.
func TestTheRuleMayFillInAMissingParameter(t *testing.T) {
	a := &Agent{allowToolFn: func(_ context.Context, req ToolCallRequest) (bool, string) {
		if _, ok := req.Params["fingerprint"]; !ok {
			req.Params["fingerprint"] = "abc123"
		}
		return true, ""
	}}

	params := map[string]any{"title": "something"}
	if allow, _ := a.allowTool(context.Background(), ToolCallRequest{Tool: "create_incident", Params: params}); !allow {
		t.Fatal("the call was refused")
	}
	if params["fingerprint"] != "abc123" {
		t.Errorf("params = %v; the filled-in value did not reach the call", params)
	}
}

// triggerOf reports what started the run. Nil means unknown — a call made
// outside a run — and a caller deciding on it must not read that as any
// particular kind of run.
func TestTriggerOfReportsUnknownAsNil(t *testing.T) {
	if triggerOf(nil) != nil {
		t.Error("no graph should be unknown")
	}
	if triggerOf(NewGraph()) != nil {
		t.Error("a graph with no context gate should be unknown")
	}
	g := NewGraph()
	g.Context = NewContextGate(g, &Trigger{Type: "alert"}, nil)
	if got := triggerOf(g); got == nil || got.Type != "alert" {
		t.Errorf("triggerOf = %v, want the run's trigger", got)
	}
}

// The engine asks last, so an application can only narrow what the gate already
// allowed. Asking before the gate would let a rule admit a call the gate would
// have refused, which nothing else would catch.
func TestTheRuleIsAskedAfterTheGate(t *testing.T) {
	src := readSource(t, "dispatcher.go")
	body := funcBody(t, src, "executeToolNode")
	gate := strings.Index(body, "CheckTriadWithScope")
	ask := strings.Index(body, "a.allowTool(")
	if gate < 0 || ask < 0 {
		t.Fatalf("cannot find both the gate (%d) and the rule (%d)", gate, ask)
	}
	if ask < gate {
		t.Error("the application's rule is asked before the engine's gate; it could admit a call the gate refuses")
	}
}

// A rule is told where the call will run, because reach is a decision this
// package does not make and an application cannot make without the fact.
//
// The two call sites mean different things by it and both are pinned here: the
// remote branch names the machine, and executeToolNode leaves it empty because
// reaching that function means the call runs on this machine, whatever the node
// happens to name.
func TestTheRuleIsToldWhereTheCallRuns(t *testing.T) {
	src := readSource(t, "dispatcher.go")

	remote := funcBody(t, src, "fireNode")
	if !strings.Contains(remote, "Target:  n.Target,") {
		t.Error("the remote branch does not tell the rule which machine the call is " +
			"aimed at, so an application cannot refuse a call for being remote")
	}

	local := funcBody(t, src, "executeToolNode")
	i := strings.Index(local, "a.allowTool(")
	if i < 0 {
		t.Fatal("executeToolNode no longer asks the rule")
	}
	if strings.Contains(local[i:], "Target:") {
		t.Error("executeToolNode names a target; reaching it means the call runs here, " +
			"and saying otherwise would have a rule refuse local calls on a node that " +
			"merely carries a target")
	}
}

// The three questions about who is asking are put to a remote step as well.
// They have no counterpart on the receiving machine: it enforces its own
// clearance and its own tool list, and knows nothing about the principal here.
func TestARemoteStepIsAskedWhoWants(t *testing.T) {
	body := funcBody(t, readSource(t, "dispatcher.go"), "fireNode")
	branch := body[strings.Index(body, "if a.remoteFor(n) {"):]
	branch = branch[:strings.Index(branch, "\n\t}\n")]

	for _, want := range []struct{ call, why string }{
		{"scopeAllows(n.ToolName, scope)", "the principal's tool list is not applied, so a restricted user is restricted here and unrestricted everywhere else"},
		{"a.checkClearance(", "the application's authorisation hook is not asked"},
		{"a.allowTool(", "the application's own rule is not asked, though it documents itself as always asked"},
	} {
		if !strings.Contains(branch, want.call) {
			t.Errorf("a remote step skips %s: %s", want.call, want.why)
		}
	}
}
