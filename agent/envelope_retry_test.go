package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A tool that reports failure the way bash does — an envelope whose status is
// error, carrying the streams — and succeeds once the command changes.
type envelopeFailingTool struct {
	calls    int
	saw      []string
	failWith string // the substring that makes a command fail
}

func (e *envelopeFailingTool) Name() string              { return "bash" }
func (e *envelopeFailingTool) Description() string       { return "for the end-to-end tests" }
func (e *envelopeFailingTool) Impact(map[string]any) int { return toolapi.ImpactAffect }
func (e *envelopeFailingTool) RequiresTarget() bool      { return false }
func (e *envelopeFailingTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (e *envelopeFailingTool) Execute(ctx context.Context, p map[string]any) (string, error) {
	return toolapi.StringResult(e.ExecuteTyped(ctx, p))
}

// Typed, because that is how bash reports and how the envelope reaches the node.
func (e *envelopeFailingTool) ExecuteTyped(_ context.Context, p map[string]any) (toolapi.ToolMessage, error) {
	cmd, _ := p["command"].(string)
	e.calls++
	e.saw = append(e.saw, cmd)
	if strings.Contains(cmd, e.failWith) {
		// Exactly the shape bash returns: no Go error, an envelope saying it failed.
		return toolapi.ToolFail("command", "command failed: exit 1: exit status 1", map[string]any{
			"exit_code": 1,
			"stdout":    "",
			"stderr":    "the input device is not a TTY\n",
			"command":   cmd,
		}), nil
	}
	return toolapi.ToolOK("command", "ran", map[string]any{
		"exit_code": 0, "stdout": "root:x:0:0\n", "stderr": "", "command": cmd,
	}), nil
}

// A step that failed gets one repair attempt, whichever way it said so.
//
// bash reports failure in its envelope rather than by returning an error, and
// only the error path reached the retry — so the engine's most-used tool was
// also its only unrepairable one. A run asking for privilege escalation had
// eight bash steps fail and not one was tried again; it then reported the
// machine was locked down, on a command that needed one flag removed.
func TestAToolThatFailsInItsEnvelopeIsStillRetried(t *testing.T) {
	tool := &envelopeFailingTool{failWith: "-it"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "operate"}},
		"plan": plan(step("bash", "exploit_docker", map[string]any{
			"command": "docker run --rm -it -v /:/host alpine cat /host/etc/shadow",
		})),
		// The fixer is called with no tools, so it answers as content.
		"":                   {Content: "docker run --rm -v /:/host alpine cat /host/etc/shadow"},
		"reflector_decision": {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentWithCompute(t, model, tool)

	res, err := a.RunDAGSync(context.Background(), operateTrigger("read the shadow file"))
	if err != nil {
		t.Fatalf("the run failed: %v (stages called: %v)", err, model.functionsCalled())
	}

	if tool.calls < 2 {
		t.Fatalf("the tool ran %d time(s) with commands %v — the failure was never retried", tool.calls, tool.saw)
	}
	if strings.Contains(tool.saw[len(tool.saw)-1], "-it") {
		t.Errorf("the retried command was %q, and still carries the flag that failed", tool.saw[len(tool.saw)-1])
	}
	if got := nodeWithTag(t, traceNodes(t, res), "exploit_docker")["state"]; got != "resolved" {
		t.Errorf("the step state is %v after a successful retry, want resolved", got)
	}
}

// The fixer is shown what the step reported, not the sentence the engine wrote
// about it.
//
// "command failed: exit 1: exit status 1" names no cause. The stderr beside it
// said "the input device is not a TTY", which names the flag to drop — and that
// is the only text a fixer can act on.
func TestTheFixerIsShownWhatTheStepActuallyReported(t *testing.T) {
	tool := &envelopeFailingTool{failWith: "-it"}
	model := newStubModel(t, map[string]stubReply{
		"submit_preflight": {Args: map[string]any{"mode": "agent", "intent": "operate"}},
		"plan": plan(step("bash", "exploit_docker", map[string]any{
			"command": "docker run --rm -it -v /:/host alpine cat /host/etc/shadow",
		})),
		"":                   {Content: "docker run --rm -v /:/host alpine cat /host/etc/shadow"},
		"reflector_decision": {Args: map[string]any{"decision": "conclude", "outcome": "done"}},
	})
	a := agentWithCompute(t, model, tool)

	if _, err := a.RunDAGSync(context.Background(), operateTrigger("read the shadow file")); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	if !model.sawUserContaining("the input device is not a TTY") {
		t.Error("the fixer was never shown the stderr, so it was asked to repair a command from an exit code alone")
	}
}
