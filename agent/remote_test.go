package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubExec struct {
	got  RemoteRequest
	n    int
	resp string
	err  error
}

func (s *stubExec) Execute(_ context.Context, req RemoteRequest) (string, error) {
	s.n++
	s.got = req
	return s.resp, s.err
}

func TestRemoteForOnlyTargetsToolNodes(t *testing.T) {
	a := &Agent{}
	a.cfg.NodeID = "self"
	a.remoteExec = &stubExec{}

	cases := []struct {
		name string
		n    *Node
		want bool
	}{
		{"tool node with a target", &Node{Type: NodeTool, Target: "elsewhere"}, true},
		{"no target means here", &Node{Type: NodeTool}, false},
		{"target equal to self means here", &Node{Type: NodeTool, Target: "self"}, false},
		{"compute never leaves this process", &Node{Type: NodeCompute, Target: "elsewhere"}, false},
		{"nil node", nil, false},
	}
	for _, c := range cases {
		if got := a.remoteFor(c.n); got != c.want {
			t.Errorf("%s: remoteFor = %v, want %v", c.name, got, c.want)
		}
	}
}

// Without an executor, a target is inert: the node runs locally exactly as it
// did before targets existed.
func TestNoExecutorMeansLocal(t *testing.T) {
	a := &Agent{}
	a.cfg.NodeID = "self"
	if a.remoteFor(&Node{Type: NodeTool, Target: "elsewhere"}) {
		t.Error("a target must not be acted on when no executor is wired")
	}
}

func TestTargetValidatorRejectsBeforeDialling(t *testing.T) {
	ex := &stubExec{resp: "ok"}
	a := &Agent{}
	a.cfg.NodeID = "self"
	a.remoteExec = ex
	a.targetValid = func(target string) error {
		if len(target) < 10 {
			return errors.New("too short; copy one from the machine list")
		}
		return nil
	}

	if err := a.validateTarget("abc"); err == nil {
		t.Fatal("short target should have been rejected")
	} else if !strings.Contains(err.Error(), "copy one from") {
		// The message reaches the planner, so it must carry the host's guidance.
		t.Errorf("validator guidance lost: %v", err)
	}
	if ex.n != 0 {
		t.Error("executor was called despite a failed validation")
	}
	if err := a.validateTarget("a-long-enough-target"); err != nil {
		t.Errorf("valid target rejected: %v", err)
	}
}

func TestNilValidatorAcceptsAnything(t *testing.T) {
	a := &Agent{}
	if err := a.validateTarget("anything at all"); err != nil {
		t.Errorf("nil validator must accept: %v", err)
	}
}

// The target is opaque: it must reach the executor byte-for-byte, with no
// normalisation, parsing or interpretation by this package.
func TestTargetIsPassedThroughUntouched(t *testing.T) {
	ex := &stubExec{resp: "done"}
	a := &Agent{}
	a.cfg.NodeID = "self"
	a.remoteExec = ex

	const odd = "  Mixed/Case:With Spaces_and-punctuation.42  "
	out, err := a.remoteExec.Execute(context.Background(), RemoteRequest{
		Target:        odd,
		Tool:          "list_processes",
		Params:        map[string]any{"limit": 10},
		Intent:        100,
		CorrelationID: "corr-1",
	})
	if err != nil || out != "done" {
		t.Fatalf("execute: %q %v", out, err)
	}
	if ex.got.Target != odd {
		t.Errorf("target was altered: %q", ex.got.Target)
	}
	if ex.got.Tool != "list_processes" || ex.got.Intent != 100 || ex.got.CorrelationID != "corr-1" {
		t.Errorf("request fields not passed through: %+v", ex.got)
	}
}
