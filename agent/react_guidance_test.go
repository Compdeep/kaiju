package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/skillmd"
)

// guidanceSkill builds a guidance-only SKILL.md, the kind an application
// registers for its own domain.
func guidanceSkill(name, body string) *skillmd.SkillMD {
	return skillmd.NewSkillMD(&skillmd.Frontmatter{Name: name, Description: "d"}, body, "", "", time.Time{}, nil)
}

// Guidance reaches a run through one store, and every stage resolves a key
// through lookupGuidanceBody. The ReAct loop read the store directly and got a
// system prompt that was complete apart from the domain knowledge it was
// supposed to bring, with nothing to say so.
func TestReActComposesTheRunsGuidance(t *testing.T) {
	a := &Agent{skillGuidance: map[string]*skillmd.SkillMD{
		"system_operations": guidanceSkill("system_operations", "OPS BODY"),
		"case_response":     guidanceSkill("case_response", "RESPONSE BODY"),
	}}

	got := a.composeGuidance([]string{"system_operations", "case_response"})

	if !strings.Contains(got, "OPS BODY") || !strings.Contains(got, "RESPONSE BODY") {
		t.Errorf("guidance is missing from the composition:\n%s", got)
	}
	if strings.Index(got, "OPS BODY") > strings.Index(got, "RESPONSE BODY") {
		t.Error("bodies should follow the order the keys were selected in")
	}
}

// A key nobody registered contributes nothing rather than a blank section, and a
// run with no guidance at all composes an empty string — so the caller can tell
// "no guidance" from "guidance that happens to be empty".
func TestComposeGuidanceIgnoresUnknownKeys(t *testing.T) {
	a := &Agent{skillGuidance: map[string]*skillmd.SkillMD{"known": guidanceSkill("known", "BODY")}}

	if got := a.composeGuidance([]string{"missing"}); got != "" {
		t.Errorf("an unknown key composed %q", got)
	}
	if got := a.composeGuidance(nil); got != "" {
		t.Errorf("no keys composed %q", got)
	}
	if got := a.composeGuidance([]string{"known", "missing"}); got != "BODY\n\n" {
		t.Errorf("composeGuidance = %q, want the known body alone", got)
	}
}

// The ReAct system prompt is assembled behind an LLM call, so this pins the call
// site: it must go through the shared resolution, not into one registry.
func TestReActSystemPromptCarriesTheGuidance(t *testing.T) {
	skill := guidanceSkill("case_response", "SKILL BODY")
	a := &Agent{skillGuidance: map[string]*skillmd.SkillMD{"case_response": skill}}

	if got := a.systemPrompt([]string{"case_response"}); !strings.Contains(got, "SKILL BODY") {
		t.Errorf("the ReAct system prompt carries no guidance:\n%s", got)
	}
	if got := a.systemPrompt(nil); strings.Contains(got, "SKILL BODY") {
		t.Error("guidance appeared for a run that selected none")
	}
}
