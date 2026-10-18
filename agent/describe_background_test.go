package agent

import (
	"strings"
	"testing"
)

// The background reaches the stages that plan and answer, and not the stage
// that classifies the request or the one that decides whether to go on.
//
// Measured on one application: a 7.7 KB host report went to preflight, the
// planner, the reflector and the verdict on every round, 314 times in 374
// calls, a fifth of everything sent — and preflight picks a mode and skills,
// the reflector reads step results. Neither decides anything from it.
func TestBackgroundReachesThePlannerAndTheAnswerOnly(t *testing.T) {
	const bg = "## Host state\n\nthe-unmistakable-background"
	a := &Agent{
		describeTrigger:    func(Trigger) string { return "look into this" },
		describeBackground: func(Trigger) string { return bg },
	}
	tr := Trigger{Type: "event", ID: "e-1"}

	if got := a.objective(tr, nil); !strings.Contains(got, bg) {
		t.Errorf("the planner's objective lacks the background:\n%s", got)
	}
	if got := a.assembleAggregatorPrompt(tr, NewGraph(), &ContextResponse{}); !strings.Contains(got, bg) {
		t.Errorf("the answer's prompt lacks the background:\n%s", got)
	}
	if got := a.formatTrigger(tr); strings.Contains(got, bg) {
		t.Errorf("the request alone, which preflight reads, carries the background:\n%s", got)
	}
	if got := a.assembleReflectorPrompt(NewGraph(), &ContextResponse{}, tr, ""); strings.Contains(got, bg) {
		t.Errorf("the reflector's prompt carries the background:\n%s", got)
	}
}

// The request comes first and the background after it, so a stage reads what
// the run is about before the material it is about.
func TestTheRequestComesBeforeItsBackground(t *testing.T) {
	a := &Agent{
		describeTrigger:    func(Trigger) string { return "the request" },
		describeBackground: func(Trigger) string { return "the background" },
	}
	got := a.formatTriggerWithBackground(Trigger{Type: "event"})
	if !strings.HasPrefix(got, "the request") || !strings.HasSuffix(got, "the background") {
		t.Errorf("got %q", got)
	}
}

// No background is the common case and changes nothing: the text is the request.
func TestNoBackgroundLeavesTheRequestAsItWas(t *testing.T) {
	a := &Agent{describeTrigger: func(Trigger) string { return "the request" }}
	if got := a.formatTriggerWithBackground(Trigger{Type: "event"}); got != "the request" {
		t.Errorf("got %q, want the request alone", got)
	}
}

// A background description that crashes loses the background and nothing else.
func TestACrashedBackgroundLeavesTheRequest(t *testing.T) {
	a := &Agent{
		describeTrigger:    func(Trigger) string { return "the request" },
		describeBackground: func(Trigger) string { panic("boom") },
	}
	if got := a.formatTriggerWithBackground(Trigger{Type: "event"}); got != "the request" {
		t.Errorf("got %q, want the request alone", got)
	}
}
