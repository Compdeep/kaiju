package agent

import "testing"

// The two modes, and the absence of a choice, are the whole of what is accepted.
func TestTheAcceptedExecutionModes(t *testing.T) {
	for _, in := range []string{ExecutionInteractive, ExecutionAutonomous, ExecutionUnset} {
		if got, ok := ParseExecutionMode(in); !ok || got != in {
			t.Errorf("ParseExecutionMode(%q) = %q, %v; want it accepted unchanged", in, got, ok)
		}
	}
}

// A near miss is refused rather than corrected. This is the failure the parser
// exists for: read by comparing against "autonomous", a typo ran interactive
// forever and said nothing, so a node configured to plan every turn routed
// every turn instead.
func TestANearMissIsRefusedRatherThanCorrected(t *testing.T) {
	for _, in := range []string{"autonomus", "Autonomous", "AUTONOMOUS", "auto", "interactve", "agent", " autonomous"} {
		if got, ok := ParseExecutionMode(in); ok {
			t.Errorf("ParseExecutionMode(%q) accepted it as %q; a typo must not choose a mode", in, got)
		}
	}
}

// A refused value returns empty, not the input, so a caller that ignores the
// second return cannot end up carrying the typo forward as if it were a mode.
func TestARefusedValueComesBackEmpty(t *testing.T) {
	if got, _ := ParseExecutionMode("autonomus"); got != "" {
		t.Errorf("got %q, want the empty string", got)
	}
}

// The listing is for error messages and capability discovery, so it names the
// choices — and not the unset value, which is the absence of one.
func TestTheListingNamesOnlyTheChoices(t *testing.T) {
	got := ExecutionModes()
	if len(got) != 2 || got[0] != ExecutionInteractive || got[1] != ExecutionAutonomous {
		t.Fatalf("ExecutionModes() = %v", got)
	}
	for _, m := range got {
		if _, ok := ParseExecutionMode(m); !ok {
			t.Errorf("ExecutionModes() lists %q, which the parser refuses", m)
		}
	}
}

// The scheduler decides by comparing against ExecutionAutonomous, and
// unattended.go by the same string. If the constant and those comparisons ever
// drift the run silently changes mode, so the value is pinned here.
func TestTheModeValuesAreTheOnesOnTheWire(t *testing.T) {
	if ExecutionInteractive != "interactive" || ExecutionAutonomous != "autonomous" {
		t.Fatalf("the wire values changed: %q / %q — every config file and client says the old ones",
			ExecutionInteractive, ExecutionAutonomous)
	}
}

// The setter refuses what the parser refuses, and leaves the mode alone when it
// does — a config door that accepted a typo and stored it would put the daemon
// back in the state the parser exists to prevent.
func TestTheSetterRefusesWhatTheParserRefuses(t *testing.T) {
	a := &Agent{}
	a.cfg.ExecutionMode = ExecutionInteractive

	if ok := a.SetExecutionMode("autonomus"); ok {
		t.Error("a mistyped mode was accepted")
	}
	if a.cfg.ExecutionMode != ExecutionInteractive {
		t.Errorf("a refused mode changed the run to %q", a.cfg.ExecutionMode)
	}
	if ok := a.SetExecutionMode(ExecutionAutonomous); !ok {
		t.Fatal("a real mode was refused")
	}
	if a.cfg.ExecutionMode != ExecutionAutonomous {
		t.Errorf("the mode is %q after being set to autonomous", a.cfg.ExecutionMode)
	}
}

// The reasoning switch takes the three values the config API validates, and
// nothing else.
func TestTheReasoningSetterTakesOnlyItsThreeValues(t *testing.T) {
	a := &Agent{}
	for _, v := range []string{"on", "off", ""} {
		if ok := a.SetReasoning(v); !ok {
			t.Errorf("SetReasoning(%q) was refused", v)
		}
		if a.llmReasoning != v {
			t.Errorf("SetReasoning(%q) left the lane on %q", v, a.llmReasoning)
		}
	}
	a.llmReasoning = "off"
	if ok := a.SetReasoning("disabled"); ok {
		t.Error("SetReasoning accepted a value the config API rejects")
	}
	if a.llmReasoning != "off" {
		t.Errorf("a refused value changed the lane to %q", a.llmReasoning)
	}
}

// A patch carries only the fields it means to change, so the ones it omits
// arrive as zero — which must leave the limit alone rather than remove it.
func TestAnOmittedLimitIsLeftAloneRatherThanZeroed(t *testing.T) {
	a := &Agent{}
	a.cfg.MaxInvestigations, a.cfg.MaxReplans = 5, 3

	a.SetPlanLimits(0, 7)
	if a.cfg.MaxInvestigations != 5 {
		t.Errorf("an omitted investigation limit became %d", a.cfg.MaxInvestigations)
	}
	if a.cfg.MaxReplans != 7 {
		t.Errorf("the replan limit is %d, want 7", a.cfg.MaxReplans)
	}

	a.SetDAGMode("")
	if a.cfg.DAGMode != "" {
		t.Errorf("an empty DAG mode wrote %q", a.cfg.DAGMode)
	}
	a.cfg.DAGMode = "orchestrator"
	a.SetDAGMode("")
	if a.cfg.DAGMode != "orchestrator" {
		t.Errorf("an omitted DAG mode cleared it to %q", a.cfg.DAGMode)
	}
}
