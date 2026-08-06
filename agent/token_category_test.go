package agent

import (
	"context"
	"strings"
	"testing"
)

// What a run's LLM spend is counted against. The built-in answer splits by
// lane — interactive versus everything else — because that is all this package
// can know. An application that runs several kinds of unattended work wants its
// bill broken down by kind, not lumped into one bucket.

func TestTokenCategoryDefaultsToTheLaneSplit(t *testing.T) {
	a := &Agent{}
	for typ, want := range map[string]string{
		"chat_query": "chat", "api_query": "chat",
		"alert": "background", "scheduled": "background", "": "background",
	} {
		if got := a.tokenCategory(Trigger{Type: typ}); got != want {
			t.Errorf("%q → %q, want %q", typ, got, want)
		}
	}
}

func TestTokenCategoryUsesTheApplicationsBuckets(t *testing.T) {
	a := &Agent{tokenCategoryFn: func(t Trigger) string {
		switch t.Type {
		case "alert":
			return "investigations"
		case "fleet_sweep":
			return "fleet"
		}
		return ""
	}}

	if got := a.tokenCategory(Trigger{Type: "alert"}); got != "investigations" {
		t.Errorf("alert → %q, want the application's bucket", got)
	}
	if got := a.tokenCategory(Trigger{Type: "fleet_sweep"}); got != "fleet" {
		t.Errorf("fleet_sweep → %q, want the application's bucket", got)
	}
	// An empty answer falls back rather than counting spend against "".
	if got := a.tokenCategory(Trigger{Type: "chat_query"}); got != "chat" {
		t.Errorf("an unclassified kind → %q, want the built-in answer", got)
	}
}

// TestTagTokensReachesEveryEntryPoint: a run tagged at one entry point and not
// another produces a bill that is quietly wrong rather than obviously missing.
func TestTagTokensReachesEveryEntryPoint(t *testing.T) {
	src := readSource(t, "scheduler.go")
	if !strings.Contains(funcBody(t, src, "RunDAGSync"), "a.tagTokens(ctx, trigger)") {
		t.Error("RunDAGSync does not tag its token usage")
	}
	if !strings.Contains(readSource(t, "loop_react.go"), "a.tagTokens(ctx, trigger)") {
		t.Error("the ReAct path does not tag its token usage")
	}
	_ = context.Background()
}
