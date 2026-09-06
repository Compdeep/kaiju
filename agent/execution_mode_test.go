package agent

import "testing"

// The two modes, and the absence of a choice, are the whole of what is accepted.
func TestTheAcceptedExecutionModes(t *testing.T) {
	for _, in := range []string{ExecutionChat, ExecutionAuto, ExecutionAgent, ExecutionUnset} {
		if got, ok := ParseExecutionMode(in); !ok || got != in {
			t.Errorf("ParseExecutionMode(%q) = %q, %v; want it accepted unchanged", in, got, ok)
		}
	}
}

// A near miss is refused rather than corrected. This is the failure the parser
// exists for: read by comparing against one name, a typo took the other branch
// forever and said nothing, so a node configured to plan every turn routed
// every turn instead.
func TestANearMissIsRefusedRatherThanCorrected(t *testing.T) {
	for _, in := range []string{"autonomus", "Agent", "AGENT", "cht", "aut", " agent", "interactve"} {
		if got, ok := ParseExecutionMode(in); ok {
			t.Errorf("ParseExecutionMode(%q) accepted it as %q; a typo must not choose a mode", in, got)
		}
	}
}

// The retired names are understood and answered with the current one, so a
// config file or a client written against them keeps working and nothing
// downstream ever sees two spellings of one mode.
func TestTheRetiredNamesAreUnderstood(t *testing.T) {
	for in, want := range map[string]string{
		"interactive": ExecutionAuto,
		"autonomous":  ExecutionAgent,
	} {
		got, ok := ParseExecutionMode(in)
		if !ok {
			t.Errorf("%q is no longer understood; every config file naming it stops loading", in)
			continue
		}
		if got != want {
			t.Errorf("%q was answered with %q, want %q", in, got, want)
		}
	}
}

// And they are not offered. "interactive" promised a run that checks in with a
// person and nothing about it ever did, so it is understood on the way in and
// never produced on the way out.
func TestTheRetiredNamesAreNotOffered(t *testing.T) {
	for _, m := range ExecutionModes() {
		if m == "interactive" || m == "autonomous" {
			t.Errorf("ExecutionModes() offers the retired name %q", m)
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
	if len(got) != 3 || got[0] != ExecutionChat || got[1] != ExecutionAuto || got[2] != ExecutionAgent {
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
	if ExecutionChat != "chat" || ExecutionAuto != "auto" || ExecutionAgent != "agent" {
		t.Fatalf("the wire values changed: %q / %q / %q — every config file and client says the old ones",
			ExecutionChat, ExecutionAuto, ExecutionAgent)
	}
}

// The setter refuses what the parser refuses, and leaves the mode alone when it
// does — a config door that accepted a typo and stored it would put the daemon
// back in the state the parser exists to prevent.
func TestTheSetterRefusesWhatTheParserRefuses(t *testing.T) {
	a := &Agent{}
	a.cfg.ExecutionMode = ExecutionAuto

	if ok := a.SetExecutionMode("autonomus"); ok {
		t.Error("a mistyped mode was accepted")
	}
	if a.cfg.ExecutionMode != ExecutionAuto {
		t.Errorf("a refused mode changed the run to %q", a.cfg.ExecutionMode)
	}
	if ok := a.SetExecutionMode(ExecutionAgent); !ok {
		t.Fatal("a real mode was refused")
	}
	if a.cfg.ExecutionMode != ExecutionAgent {
		t.Errorf("the mode is %q after being set to agent", a.cfg.ExecutionMode)
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
