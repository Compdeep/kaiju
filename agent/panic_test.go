package agent

import (
	"context"
	"strings"
	"testing"
)

// A panic in application code must not end the process, and each capability has
// its own safe answer — the one thing a shared guard could not have given them.
//
// These run the real wrappers, so a missing recover fails the test by panicking
// rather than by returning the wrong value.
func TestApplicationCodeThatPanicsDoesNotEndTheRun(t *testing.T) {
	boom := func() { panic("application fault") }

	t.Run("a crashed admission rule admits, as a missing one does", func(t *testing.T) {
		a := &Agent{admitRun: func(Trigger) (bool, string) { boom(); return false, "" }}
		ok, reason := a.admit(Trigger{})
		if !ok || reason != "" {
			t.Errorf("admit = %v, %q; want the run admitted", ok, reason)
		}
	})

	t.Run("a crashed tool rule refuses, because it has not said yes", func(t *testing.T) {
		a := &Agent{allowToolFn: func(context.Context, ToolCallRequest) (bool, string) { boom(); return true, "" }}
		allow, reason := a.allowTool(context.Background(), ToolCallRequest{Tool: "create_incident"})
		if allow {
			t.Error("a rule that crashed allowed a state-changing call")
		}
		if !strings.Contains(reason, "create_incident") {
			t.Errorf("reason = %q; the model is told nothing", reason)
		}
	})

	t.Run("a crashed answer leaves the run to the aggregator", func(t *testing.T) {
		a := &Agent{answer: func(context.Context, AnswerRequest) (*AnswerResult, error) { boom(); return nil, nil }}
		res, ok, err := a.writeAnswer(context.Background(), AnswerRequest{})
		if ok || res != nil {
			t.Errorf("writeAnswer = %v, %v; want the run declined", res, ok)
		}
		if err != nil {
			t.Errorf("err = %v; a crash is not a failed run", err)
		}
	})

	t.Run("a crashed watching rule reads the trigger instead", func(t *testing.T) {
		a := &Agent{isUnattended: func(Trigger) bool { boom(); return false }}
		if !a.unattended(Trigger{ExecutionMode: "autonomous"}) {
			t.Error("an autonomous run was treated as watched")
		}
		if a.unattended(Trigger{Type: "chat_query"}) {
			t.Error("a chat query was treated as unwatched")
		}
	})

	t.Run("a crashed naming rule uses the built-in names", func(t *testing.T) {
		a := &Agent{tokenCategoryFn: func(Trigger) string { boom(); return "" }}
		if got := a.tokenCategory(Trigger{Type: "chat_query"}); got != "chat" {
			t.Errorf("tokenCategory = %q, want the built-in name", got)
		}
	})

	t.Run("a crashed target check rejects the target", func(t *testing.T) {
		a := &Agent{targetValid: func(string) error { boom(); return nil }}
		if err := a.validateTarget("peer-1"); err == nil {
			t.Error("a check that crashed approved a target for a connection")
		}
	})

	t.Run("a crashed machine lister falls back to the run's target", func(t *testing.T) {
		a := &Agent{targetLister: func(Trigger) []string { boom(); return nil }}
		got := a.runTargets(Trigger{Target: "peer-2"})
		if len(got) != 1 || got[0] != "peer-2" {
			t.Errorf("runTargets = %v, want the run's own target", got)
		}
	})

	t.Run("crashed wording uses the built-in wording", func(t *testing.T) {
		a := &Agent{describeTrigger: func(Trigger) string { boom(); return "" }}
		if got := a.formatTrigger(Trigger{Type: "alert", AlertID: "a-1"}); got == "" {
			t.Error("every reasoning stage would read nothing")
		}
	})
}

// TestEveryCallIntoApplicationCodeIsGuarded is the one that matters. Every test
// above passes on a wrapper written today; this catches the tenth capability
// somebody adds later, because the failure mode is forgetting entirely — which
// is how two of these came to be written without a guard in the first place.
func TestEveryCallIntoApplicationCodeIsGuarded(t *testing.T) {
	// Each entry is the wrapper this package calls application code through.
	for _, c := range []struct{ file, fn string }{
		{"admission.go", "admit"},
		{"allowtool.go", "allowTool"},
		{"answer.go", "writeAnswer"},
		{"unattended.go", "unattended"},
		{"token_category.go", "tokenCategory"},
		{"remote.go", "validateTarget"},
		{"remote.go", "runTargets"},
		{"loop_react.go", "formatTrigger"},
		{"refine.go", "refinePreflight"},
	} {
		body := funcBody(t, readSource(t, c.file), c.fn)
		if !strings.Contains(body, "recover()") {
			t.Errorf("%s calls application code with no guard: a panic there ends the process", c.fn)
		}
	}
}
