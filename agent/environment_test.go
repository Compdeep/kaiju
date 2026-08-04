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
	got := a.fleetSection()
	if !strings.Contains(got, "alpha") {
		t.Fatalf("Config.Environment did not reach the prompt section: %q", got)
	}
	if !strings.HasPrefix(got, "\n\n") {
		t.Errorf("expected a leading blank line so it appends cleanly: %q", got)
	}
}

func TestEmptyEnvironmentAddsNothing(t *testing.T) {
	a := &Agent{environment: func() string { return "" }}
	if got := a.fleetSection(); got != "" {
		t.Errorf("an empty description should add nothing, got %q", got)
	}
	b := &Agent{}
	if got := b.fleetSection(); got != "" {
		t.Errorf("no description at all should add nothing, got %q", got)
	}
}

// Environment takes precedence: an application that has moved to it should not
// also get the deprecated fleet text.
func TestEnvironmentWinsOverFleet(t *testing.T) {
	a := &Agent{
		environment: func() string { return "from environment" },
		fleet:       stubFleet{"from fleet"},
	}
	if got := a.fleetSection(); !strings.Contains(got, "environment") {
		t.Errorf("Environment should take precedence, got %q", got)
	}
}

type stubFleet struct{ s string }

func (f stubFleet) FleetContext() string { return f.s }
