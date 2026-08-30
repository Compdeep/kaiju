package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The engine holds no view about HTTP, or about any other protocol a tool
// speaks. It asks the tool that failed and does what it is told, and when it is
// told nothing it classifies the error text exactly as it always has.
//
// These tests use a tool that exists only here, for that reason: what is being
// checked is the asking, not what any particular tool answers. web_fetch's own
// answers are checked in tools, where the knowledge behind them lives.

type opinionatedTool struct {
	name   string
	advice toolapi.RetryAdvice
}

func (o *opinionatedTool) Name() string                { return o.name }
func (o *opinionatedTool) Description() string         { return "" }
func (o *opinionatedTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (o *opinionatedTool) Impact(map[string]any) int   { return 0 }
func (o *opinionatedTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}
func (o *opinionatedTool) RetryAdvice(toolapi.ToolMessage) toolapi.RetryAdvice { return o.advice }

var _ toolapi.Tool = (*opinionatedTool)(nil)
var _ toolapi.Retryable = (*opinionatedTool)(nil)

// silentTool declares nothing, which is what almost every tool does.
type silentTool struct{ name string }

func (s *silentTool) Name() string                { return s.name }
func (s *silentTool) Description() string         { return "" }
func (s *silentTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (s *silentTool) Impact(map[string]any) int   { return 0 }
func (s *silentTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

var _ toolapi.Tool = (*silentTool)(nil)

func agentHolding(t *testing.T, tools ...toolapi.Tool) *Agent {
	t.Helper()
	reg := toolapi.NewRegistry()
	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name(), err)
		}
	}
	return &Agent{registry: reg}
}

func failedCompletion(detail string) nodeCompletion {
	msg := toolapi.ToolFail("page", detail, nil)
	return nodeCompletion{NodeID: "n1", Body: NewToolBody(msg)}
}

func TestToolRetryAdviceAsksTheToolThatFailed(t *testing.T) {
	cases := []struct {
		name   string
		tool   toolapi.Tool
		node   *Node
		want   toolapi.RetryVerdict
		andFor time.Duration
	}{
		{
			name: "the tool says do not bother",
			tool: &opinionatedTool{name: "walled", advice: toolapi.RetryAdvice{Verdict: toolapi.RetryNever, Why: "a challenge"}},
			node: &Node{Type: NodeTool, ToolName: "walled"},
			want: toolapi.RetryNever,
		},
		{
			name:   "the tool says wait first",
			tool:   &opinionatedTool{name: "busy", advice: toolapi.RetryAdvice{Verdict: toolapi.RetryAfter, Wait: 9 * time.Second}},
			node:   &Node{Type: NodeTool, ToolName: "busy"},
			want:   toolapi.RetryAfter,
			andFor: 9 * time.Second,
		},
		{
			name: "a tool that declares nothing",
			tool: &silentTool{name: "quiet"},
			node: &Node{Type: NodeTool, ToolName: "quiet"},
			want: toolapi.RetryUnknown,
		},
		{
			name: "a node that is not a tool node",
			tool: &opinionatedTool{name: "walled", advice: toolapi.RetryAdvice{Verdict: toolapi.RetryNever}},
			node: &Node{Type: NodeCompute, ToolName: "walled"},
			want: toolapi.RetryUnknown,
		},
		{
			name: "a tool the registry does not hold",
			tool: &silentTool{name: "quiet"},
			node: &Node{Type: NodeTool, ToolName: "vanished"},
			want: toolapi.RetryUnknown,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := agentHolding(t, c.tool)
			got := a.toolRetryAdvice(c.node, failedCompletion("HTTP 429 429 Too Many Requests"))
			if got.Verdict != c.want {
				t.Fatalf("verdict = %v, want %v", got.Verdict, c.want)
			}
			if c.andFor != 0 && got.Wait != c.andFor {
				t.Errorf("wait = %s, want %s", got.Wait, c.andFor)
			}
		})
	}
}

// A completion with no typed body is every legacy string tool, and asking one
// must not panic or invent a verdict.
func TestToolRetryAdviceSurvivesAnUntypedResult(t *testing.T) {
	a := agentHolding(t, &opinionatedTool{name: "walled", advice: toolapi.RetryAdvice{Verdict: toolapi.RetryNever}})
	got := a.toolRetryAdvice(&Node{Type: NodeTool, ToolName: "walled"}, nodeCompletion{NodeID: "n1"})
	if got.Verdict != toolapi.RetryUnknown {
		t.Errorf("verdict = %v, want RetryUnknown", got.Verdict)
	}
}

// The engine's own classification is untouched — it is what runs whenever a
// tool has no view, which is the common case.
func TestTextClassificationStillRunsUnderneath(t *testing.T) {
	if got := classifyRetryTier("HTTP 429 Too Many Requests"); got != "blind" {
		t.Errorf("a rate limit classifies as %q, not blind", got)
	}
	if got := classifyRetryTier("command not found"); got != "skip" {
		t.Errorf("a missing command classifies as %q, not skip", got)
	}
}
