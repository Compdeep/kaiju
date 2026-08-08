package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/skillmd"
)

// One renderer serves every stage. It takes two sections from each card: the one
// this stage reads, and "## Core Principles", which every stage gets — so a
// principle is written once in the card rather than repeated under each heading.
func TestGuidanceSectionTakesCorePrinciplesAndTheStagesOwn(t *testing.T) {
	a := &Agent{capabilities: CapabilityRegistry{"triage": {Key: "triage", Body: "" +
		"## Core Principles\n\nsay what you cannot see\n\n" +
		"## Aggregator Guidance\n\nrate it honestly\n\n" +
		"## Classifier Guidance\n\nsuppress signed vendor noise\n"}}}

	got := a.GuidanceSection([]string{"triage"}, "## Aggregator Guidance", "aggregator doctrine")

	for _, want := range []string{
		"## Skill Guidance (authoritative — apply these principles)",
		"### triage — aggregator doctrine",
		"say what you cannot see",
		"rate it honestly",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q from:\n%s", want, got)
		}
	}
	if strings.Contains(got, "suppress signed vendor noise") {
		t.Error("another stage's section leaked in")
	}
}

// The same card renders differently for a different stage, which is why the
// heading and the label are arguments rather than fixed.
func TestOneCardServesTwoStages(t *testing.T) {
	a := &Agent{capabilities: CapabilityRegistry{"triage": {Key: "triage", Body: "" +
		"## Aggregator Guidance\n\nrate it honestly\n\n" +
		"## Classifier Guidance\n\nsuppress signed vendor noise\n"}}}

	agg := a.GuidanceSection([]string{"triage"}, "## Aggregator Guidance", "aggregator doctrine")
	cls := a.GuidanceSection([]string{"triage"}, "## Classifier Guidance", "classifier doctrine")

	if !strings.Contains(agg, "rate it honestly") || strings.Contains(agg, "suppress signed") {
		t.Errorf("aggregator got the wrong section:\n%s", agg)
	}
	if !strings.Contains(cls, "suppress signed") || strings.Contains(cls, "rate it honestly") {
		t.Errorf("classifier got the wrong section:\n%s", cls)
	}
	if !strings.Contains(cls, "### triage — classifier doctrine") {
		t.Errorf("the entry is not labelled for this stage:\n%s", cls)
	}
}

// Doctrine registered as a SKILL.md skill renders the same as a card. Before
// this, the aggregator read the card registry alone, so an application with only
// skills got a prompt with no domain text and nothing said so.
func TestGuidanceSectionReadsBothRegistries(t *testing.T) {
	a := &Agent{
		capabilities:  CapabilityRegistry{"triage": {Key: "triage", Body: "## Aggregator Guidance\n\nfrom a card\n"}},
		skillGuidance: map[string]*skillmd.SkillMD{"response": guidanceSkill("response", "## Aggregator Guidance\n\nfrom a skill\n")},
	}

	got := a.GuidanceSection([]string{"triage", "response"}, "## Aggregator Guidance", "aggregator doctrine")

	if !strings.Contains(got, "from a card") || !strings.Contains(got, "from a skill") {
		t.Errorf("one of the two registries was skipped:\n%s", got)
	}
	if strings.Index(got, "from a card") > strings.Index(got, "from a skill") {
		t.Error("entries are not in the order the run selected them")
	}
}

// Nothing to say produces nothing at all. A heading with an empty body under it
// tells the model doctrine applies when none does.
func TestGuidanceSectionIsSilentWithNothingToSay(t *testing.T) {
	a := &Agent{capabilities: CapabilityRegistry{"triage": {Key: "triage", Body: "## Planner Guidance\n\nelsewhere\n"}}}

	for _, got := range []string{
		a.GuidanceSection(nil, "## Aggregator Guidance", "aggregator doctrine"),
		a.GuidanceSection([]string{"triage"}, "## Aggregator Guidance", "aggregator doctrine"),
		a.GuidanceSection([]string{"never_registered"}, "## Aggregator Guidance", "aggregator doctrine"),
	} {
		if got != "" {
			t.Errorf("rendered %q with nothing to say", got)
		}
	}
}

// TestTheAggregatorUsesTheOneRenderer guards the call site. It read the card
// registry directly before, which is how it came to miss both the skills and
// the Core Principles block.
func TestTheAggregatorUsesTheOneRenderer(t *testing.T) {
	body := funcBody(t, readSource(t, "aggregator.go"), "runAggregatorWithClient")
	if !strings.Contains(body, "a.GuidanceSection(cards,") {
		t.Error("the aggregator no longer renders through the shared renderer")
	}
}
