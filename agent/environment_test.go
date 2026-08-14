package agent

import (
	"strings"
	"testing"
)

// Config.Environment must actually reach the prompts. It was possible to set it
// and have nothing happen — no error, no effect — which is the worst kind of
// broken for a configuration field.
func TestEnvironmentReachesPrompts(t *testing.T) {
	a := &Agent{environment: func() string { return "machines: alpha, beta" }}
	got := a.environmentSection()
	if !strings.Contains(got, "alpha") {
		t.Fatalf("Config.Environment did not reach the prompt section: %q", got)
	}
	if !strings.HasPrefix(got, "\n\n") {
		t.Errorf("expected a leading blank line so it appends cleanly: %q", got)
	}
}

func TestEmptyEnvironmentAddsNothing(t *testing.T) {
	a := &Agent{environment: func() string { return "" }}
	if got := a.environmentSection(); got != "" {
		t.Errorf("an empty description should add nothing, got %q", got)
	}
	b := &Agent{}
	if got := b.environmentSection(); got != "" {
		t.Errorf("no description at all should add nothing, got %q", got)
	}
}

// A description that crashes costs a paragraph, not the run. Seven prompt
// builders call this, so the alternative is one bad application function ending
// every stage.
func TestACrashingDescriptionAddsNothing(t *testing.T) {
	a := &Agent{environment: func() string { panic("no") }}
	if got := a.environmentSection(); got != "" {
		t.Errorf("a panicking description should add nothing, got %q", got)
	}
}
