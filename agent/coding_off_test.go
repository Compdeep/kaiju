package agent

import (
	"context"
	"strings"
	"testing"
)

// With coding off, deep compute is refused at dispatch (compute.go). Nothing
// the planner reads may still offer it: the tool's own schema said "deep:
// plans approach first then implements", the planner prompt told it to obey a
// card that says "use compute deep", and on runs 457eb7b7 and a9489bca
// (2026-11-16) both planners chose deep for a plain calculation and failed.

func TestCodingOff_ComputeOffersShallowOnly(t *testing.T) {
	off := NewComputeTool(&Agent{cfg: Config{ComputeConfig: ComputeConfig{DisableCoding: true}}})
	params := string(off.Parameters())
	if !strings.Contains(params, `"enum": ["shallow"]`) || strings.Contains(params, `"deep"`) {
		t.Errorf("compute's schema still offers deep with coding off: %s", params)
	}
	if strings.Contains(off.Description(), "Deep mode") {
		t.Errorf("compute's description still offers deep with coding off: %s", off.Description())
	}

	on := NewComputeTool(&Agent{})
	if !strings.Contains(string(on.Parameters()), `["shallow","deep"]`) {
		t.Error("compute lost deep with coding on")
	}
}

func TestCodingOff_SkillGuidanceDoesNotOrderDeep(t *testing.T) {
	g := NewGraph()
	g.ActiveCards = []string{"data_retrieval"}

	off := &Agent{cfg: Config{ComputeConfig: ComputeConfig{DisableCoding: true}}, skillGuidance: loadBuiltinSkills(nil)}
	p := off.executiveSystemPrompt(context.Background(), g, nil, "", "observe", "")
	if strings.Contains(p, `If a skill says "use compute deep", use compute deep`) {
		t.Error("the planner is told to follow a card's compute deep while deep is refused")
	}
	if !strings.Contains(p, "compute(deep) is switched off") {
		t.Error("the planner is not told deep is switched off")
	}

	on := &Agent{skillGuidance: loadBuiltinSkills(nil)}
	if p := on.executiveSystemPrompt(context.Background(), g, nil, "", "observe", ""); !strings.Contains(p, `If a skill says "use compute deep", use compute deep`) {
		t.Error("with coding on the planner lost the card rule")
	}
}
