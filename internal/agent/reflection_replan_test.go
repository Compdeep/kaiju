package agent

import (
	"strings"
	"testing"
)

// TestParseReflectionOutput_Replan covers the EXPAND decision added in Phase 1
// of the replan design: the reflector may emit "replan" with a `next` field
// naming the move the executive should plan. Verifies the decision is accepted
// and `next` round-trips.
func TestParseReflectionOutput_Replan(t *testing.T) {
	raw := `{"decision":"replan","progress":"productive","summary":"3 searches returned URLs","next":"fetch the 3 URLs the searches surfaced"}`
	out, err := parseReflectionOutput(raw)
	if err != nil {
		t.Fatalf("parseReflectionOutput errored on valid replan: %v", err)
	}
	if out.Decision != "replan" {
		t.Fatalf("decision = %q, want replan", out.Decision)
	}
	if out.Next != "fetch the 3 URLs the searches surfaced" {
		t.Fatalf("next = %q, want the fetch move", out.Next)
	}
	if out.Progress != "productive" {
		t.Fatalf("progress = %q, want productive", out.Progress)
	}
}

// TestParseReflectionOutput_DecisionSet locks the accepted decision set: the
// three valid decisions parse, and an unknown one is still rejected (so a
// hallucinated decision can't silently drive the scheduler).
func TestParseReflectionOutput_DecisionSet(t *testing.T) {
	for _, d := range []string{"continue", "replan", "conclude"} {
		if _, err := parseReflectionOutput(`{"decision":"` + d + `","summary":"s"}`); err != nil {
			t.Errorf("decision %q should be valid, got error: %v", d, err)
		}
	}
	if _, err := parseReflectionOutput(`{"decision":"teleport","summary":"s"}`); err == nil {
		t.Errorf("unknown decision should be rejected")
	}
}

// TestParseReflectionOutput_InvestigateCoerced verifies the removed `investigate`
// decision is coerced into `replan`, folding its `problem` into `next`, so an
// old prompt or model can't break the scheduler (repair now flows through
// replan → the executive plans a debug step).
func TestParseReflectionOutput_InvestigateCoerced(t *testing.T) {
	out, err := parseReflectionOutput(`{"decision":"investigate","summary":"build broke","problem":"main.go:10 undefined: Foo"}`)
	if err != nil {
		t.Fatalf("investigate should coerce, not error: %v", err)
	}
	if out.Decision != "replan" {
		t.Fatalf("decision = %q, want replan (coerced)", out.Decision)
	}
	if out.Next != "main.go:10 undefined: Foo" {
		t.Fatalf("next = %q, want the problem folded in", out.Next)
	}
}

// TestScaleReplanCap verifies the replan ceiling auto-scales off the initial
// plan's difficulty: small plans keep the base, bigger/compute-heavier plans
// earn headroom, the base is a floor, and a huge plan can't blow past the ceiling.
func TestScaleReplanCap(t *testing.T) {
	mk := func(n int, computes int) []PlanStep {
		steps := make([]PlanStep, 0, n)
		for i := 0; i < computes; i++ {
			steps = append(steps, PlanStep{Tool: "compute"})
		}
		for i := computes; i < n; i++ {
			steps = append(steps, PlanStep{Tool: "web_search"})
		}
		return steps
	}
	cases := []struct {
		name  string
		base  int
		steps []PlanStep
		want  int
	}{
		{"tiny lookup keeps base", 3, mk(2, 0), 3},          // 2/4=0
		{"8 plain steps add two", 3, mk(8, 0), 5},           // 8/4=2
		{"compute steps each add one", 3, mk(4, 3), 3 + 1 + 3}, // 4/4=1 + 3 computes
		{"base is a floor", 8, mk(1, 0), 8},                 // 1/4=0, floor holds
		{"huge plan clamped at ceiling", 3, mk(100, 0), 12}, // 100/4=25 → clamp 12
		{"high base raises ceiling", 15, mk(1, 0), 15},      // base above 12 stays
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := scaleReplanCap(c.base, c.steps); got != c.want {
				t.Fatalf("scaleReplanCap(base=%d, %d steps) = %d, want %d", c.base, len(c.steps), got, c.want)
			}
		})
	}
}

// TestAssembleReflectorPrompt_Budget verifies the budget-in-English line is
// injected as a "## Budget" section when present and omitted when empty, so the
// reflector self-regulates against the replan/investigate caps.
func TestAssembleReflectorPrompt_Budget(t *testing.T) {
	trig := Trigger{Type: "chat_query", Data: []byte(`{"query":"find the CVEs"}`)}

	withBudget := assembleReflectorPrompt(nil, nil, trig, "replan round 2 of 3, 3m40s elapsed.")
	if !strings.Contains(withBudget, "## Budget") || !strings.Contains(withBudget, "replan round 2 of 3") {
		t.Fatalf("budget line not injected:\n%s", withBudget)
	}

	noBudget := assembleReflectorPrompt(nil, nil, trig, "")
	if strings.Contains(noBudget, "## Budget") {
		t.Fatalf("empty budget line should not emit a Budget section:\n%s", noBudget)
	}
}
