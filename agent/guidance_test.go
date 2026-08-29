package agent

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Compdeep/kaiju/agent/skillmd"
)

// One renderer serves every stage. It takes two sections from each card: the one
// this stage reads, and "## Core Principles", which every stage gets — so a
// principle is written once in the card rather than repeated under each heading.
func TestGuidanceSectionTakesCorePrinciplesAndTheStagesOwn(t *testing.T) {
	a := &Agent{skillGuidance: map[string]*skillmd.SkillMD{"review": guidanceSkill("review", ""+
		"## Core Principles\n\nsay what you cannot see\n\n"+
		"## Aggregator Guidance\n\nrate it honestly\n\n"+
		"## Classifier Guidance\n\nsuppress signed vendor noise\n")}}

	got := a.GuidanceSection([]string{"review"}, "## Aggregator Guidance", "aggregator doctrine")

	for _, want := range []string{
		"## Skill Guidance (authoritative — apply these principles)",
		"### review — aggregator doctrine",
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
	a := &Agent{skillGuidance: map[string]*skillmd.SkillMD{"review": guidanceSkill("review", ""+
		"## Aggregator Guidance\n\nrate it honestly\n\n"+
		"## Classifier Guidance\n\nsuppress signed vendor noise\n")}}

	agg := a.GuidanceSection([]string{"review"}, "## Aggregator Guidance", "aggregator doctrine")
	cls := a.GuidanceSection([]string{"review"}, "## Classifier Guidance", "classifier doctrine")

	if !strings.Contains(agg, "rate it honestly") || strings.Contains(agg, "suppress signed") {
		t.Errorf("aggregator got the wrong section:\n%s", agg)
	}
	if !strings.Contains(cls, "suppress signed") || strings.Contains(cls, "rate it honestly") {
		t.Errorf("classifier got the wrong section:\n%s", cls)
	}
	if !strings.Contains(cls, "### review — classifier doctrine") {
		t.Errorf("the entry is not labelled for this stage:\n%s", cls)
	}
}

// Doctrine registered as a SKILL.md skill renders the same as a card. Before
// this, the aggregator read the card registry alone, so an application with only
// skills got a prompt with no domain text and nothing said so.
func TestGuidanceSectionReadsBothRegistries(t *testing.T) {
	a := &Agent{
		skillGuidance: map[string]*skillmd.SkillMD{"review": guidanceSkill("review", "## Aggregator Guidance\n\nfrom a card\n"), "response": guidanceSkill("response", "## Aggregator Guidance\n\nfrom a skill\n")},
	}

	got := a.GuidanceSection([]string{"review", "response"}, "## Aggregator Guidance", "aggregator doctrine")

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
	a := &Agent{skillGuidance: map[string]*skillmd.SkillMD{"review": guidanceSkill("review", "## Planner Guidance\n\nelsewhere\n")}}

	for _, got := range []string{
		a.GuidanceSection(nil, "## Aggregator Guidance", "aggregator doctrine"),
		a.GuidanceSection([]string{"review"}, "## Aggregator Guidance", "aggregator doctrine"),
		a.GuidanceSection([]string{"never_registered"}, "## Aggregator Guidance", "aggregator doctrine"),
	} {
		if got != "" {
			t.Errorf("rendered %q with nothing to say", got)
		}
	}
}

// The gate now serves the answer-writing layout, and it must be the same text
// the stages produced when they built it themselves. Moving where a prompt comes
// from is not licence to change what it says.
func TestTheGateProducesTheSameGuidanceTextAsBefore(t *testing.T) {
	a := &Agent{skillGuidance: map[string]*skillmd.SkillMD{"review": guidanceSkill("review", ""+
		"## Core Principles\n\nsay what you cannot see\n\n"+
		"## Aggregator Guidance\n\nrate it honestly\n")}}
	g := NewGraph()
	g.ActiveCards = []string{"review"}
	g.Context = NewContextGate(g, &Trigger{}, a)

	direct := a.GuidanceSection(g.ActiveCards, "## Aggregator Guidance", "aggregator doctrine")

	resp, err := g.Context.Get(context.Background(), ContextRequest{
		ReturnSources:   Sources(LabelledGuidance("## Aggregator Guidance", "aggregator doctrine")),
		MaxBudget:       6000,
		OmitCurrentTime: true,
	})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	viaGate := resp.Sources[SourceSkillGuidance]

	if viaGate != direct {
		t.Errorf("the gate changed the wording.\n via gate:\n%s\n direct:\n%s", viaGate, direct)
	}
	if !strings.Contains(viaGate, "say what you cannot see") {
		t.Error("Core Principles did not survive the move")
	}
}

// The terser layout the compute and investigation stages already use is
// untouched: no preamble, no label, no principles.
func TestThePlainLayoutIsUnchanged(t *testing.T) {
	a := &Agent{skillGuidance: map[string]*skillmd.SkillMD{"review": guidanceSkill("review", ""+
		"## Core Principles\n\nsay what you cannot see\n\n"+
		"## Debug Guidance\n\nread the stack first\n")}}
	g := NewGraph()
	g.ActiveCards = []string{"review"}
	g.Context = NewContextGate(g, &Trigger{}, a)

	resp, err := g.Context.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(SkillGuidance([]string{"Debug"})),
		MaxBudget:     6000,
	})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	got := resp.Sources[SourceSkillGuidance]

	if !strings.Contains(got, "### review") || !strings.Contains(got, "#### Debug Guidance") {
		t.Errorf("the plain layout changed:\n%s", got)
	}
	if strings.Contains(got, "authoritative") || strings.Contains(got, "say what you cannot see") {
		t.Errorf("the answer-writing layout leaked into the plain one:\n%s", got)
	}
}

// TestTheAggregatorAsksTheGate guards the call site. Asking directly still
// works, so nothing fails if this reverts — the guidance simply stops being
// held to a budget, and there are two paths to the same text again.
func TestTheAggregatorAsksTheGate(t *testing.T) {
	body := funcBody(t, readSource(t, "aggregator.go"), "runAggregator")
	if !strings.Contains(body, `LabelledGuidance("## Aggregator Guidance", "aggregator doctrine")`) {
		t.Error("the aggregator no longer asks the gate for its doctrine")
	}
}

// The four cards compiled into the binary are the baseline: an installation with
// no data directory and no SKILL.md files of its own still has guidance for the
// ordinary kinds of request.
func TestTheBuiltInSkillCardsLoad(t *testing.T) {
	cards := loadBuiltinSkills(nil)

	for _, key := range []string{"data_retrieval", "general_reasoning", "self_awareness", "system_operations"} {
		card, ok := cards[key]
		if !ok {
			t.Errorf("%s did not load", key)
			continue
		}
		if card.Description() == "" {
			t.Errorf("%s has no description, so the model cannot choose it", key)
		}
		if !strings.Contains(card.Body(), "## Planning Guidance") {
			t.Errorf("%s has no planning guidance", key)
		}
	}
}

// TestTheBuiltInCardsReachThePlanner is the point of the merge. Their
// "## Planning Guidance" sections were written for a planner that read the other
// store and never saw them — four files of advice, read by nothing that plans.
func TestTheBuiltInCardsReachThePlanner(t *testing.T) {
	a := &Agent{skillGuidance: loadBuiltinSkills(nil), soulPrompt: ""}
	g := NewGraph()
	g.ActiveCards = []string{"data_retrieval"}

	prompt := a.executiveSystemPrompt(context.Background(), g, nil, "", "observe", "")

	if !strings.Contains(prompt, "ask for what the question needs") {
		t.Errorf("the data_retrieval card's planning guidance did not reach the planner:\n%s", prompt)
	}
	if !strings.Contains(prompt, "### data_retrieval") {
		t.Error("the card is not named in the planner prompt")
	}
}

// With nothing selected the planner takes every card, as it always has. Falling
// back to none would strip its guidance on any run where selection produced
// nothing.
func TestNoSelectionGivesThePlannerEveryCard(t *testing.T) {
	a := &Agent{skillGuidance: loadBuiltinSkills(nil)}

	prompt := a.executiveSystemPrompt(context.Background(), NewGraph(), nil, "", "observe", "")

	for _, key := range []string{"data_retrieval", "system_operations"} {
		if !strings.Contains(prompt, "### "+key) {
			t.Errorf("%s missing from a run that selected nothing", key)
		}
	}
}

// An application's own card is loaded, and replaces the engine's of that name.
//
// This is what lets an application compile in the guidance for its own kind of
// work without forking the package: the engine's cards are the baseline and the
// application's are read over them. Without it, an application that embeds this
// engine ships the engine's cards and none of its own.
func TestAnApplicationsOwnSkillCardsAreLoaded(t *testing.T) {
	own := fstest.MapFS{
		"pest_control/SKILL.md": &fstest.MapFile{Data: []byte(
			"---\nname: pest_control\ndescription: what to do about pests\n---\n\nCall someone.\n")},
		"general_reasoning/SKILL.md": &fstest.MapFile{Data: []byte(
			"---\nname: general_reasoning\ndescription: replaced\n---\n\nMine, not yours.\n")},
	}
	cards := loadBuiltinSkills(own)

	if _, ok := cards["pest_control"]; !ok {
		t.Error("the application's own card did not load, so an application embedding " +
			"this engine ships the engine's guidance and none of its own")
	}
	if len(cards) < 4 {
		t.Errorf("loaded %d cards; the engine's own should still be there", len(cards))
	}
	replaced, ok := cards["general_reasoning"]
	if !ok {
		t.Fatal("the engine's card vanished rather than being replaced")
	}
	if !strings.Contains(replaced.Body(), "Mine, not yours") {
		t.Error("a card sharing a name with the engine's did not replace it, so an " +
			"application cannot override guidance it disagrees with")
	}
}
