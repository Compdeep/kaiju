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

// Guidance reaches a run through two registries: capability cards, and SKILL.md
// guidance skills an application registers. Every stage that needs a key's body
// resolves it through lookupGuidanceBody, which knows about both.
//
// The ReAct loop read the card registry directly. A run whose guidance came from
// a skill got a system prompt that was complete apart from the domain knowledge
// it was supposed to bring, and nothing said so — which is what an application
// with only skills, and no cards, would have got for every run.
func TestReActComposesGuidanceFromBothRegistries(t *testing.T) {
	skill := guidanceSkill("incident_response", "SKILL BODY")
	a := &Agent{
		capabilities:  CapabilityRegistry{"system_operations": {Key: "system_operations", Body: "CARD BODY"}},
		skillGuidance: map[string]*skillmd.SkillMD{"incident_response": skill},
	}

	got := a.composeGuidance([]string{"system_operations", "incident_response"})

	if !strings.Contains(got, "CARD BODY") {
		t.Error("a capability card's guidance is missing")
	}
	if !strings.Contains(got, "SKILL BODY") {
		t.Error("a guidance skill's body is missing; an application with only skills composes nothing")
	}
	if strings.Index(got, "CARD BODY") > strings.Index(got, "SKILL BODY") {
		t.Error("bodies should follow the order the keys were selected in")
	}
}

// A key nobody registered contributes nothing rather than a blank section, and a
// run with no guidance at all composes an empty string — so the caller can tell
// "no guidance" from "guidance that happens to be empty".
func TestComposeGuidanceIgnoresUnknownKeys(t *testing.T) {
	a := &Agent{capabilities: CapabilityRegistry{"known": {Key: "known", Body: "BODY"}}}

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
	skill := guidanceSkill("incident_response", "SKILL BODY")
	a := &Agent{skillGuidance: map[string]*skillmd.SkillMD{"incident_response": skill}}

	if got := a.systemPrompt([]string{"incident_response"}); !strings.Contains(got, "SKILL BODY") {
		t.Errorf("the ReAct system prompt carries no guidance:\n%s", got)
	}
	if got := a.systemPrompt(nil); strings.Contains(got, "SKILL BODY") {
		t.Error("guidance appeared for a run that selected none")
	}
}
