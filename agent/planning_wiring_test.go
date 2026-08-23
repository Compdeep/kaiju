package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/prompt"
)

// These guard the regression where the planner stopped emitting wired multi-step
// plans (`${step.N.field}` + `depends_on`) and fell back to flat plans with literal
// URLs invented from memory. The cause was prompt TEXT that told the planner not to
// wire — in the replan frame and in a bundled skill. These tests assert the prompts
// the planner actually receives keep teaching wiring and never ban it.

// The replan frame must lead with a wired chain and must not re-introduce the
// "Do NOT use ${step}" ban that collapsed replans to flat plans (commit 2fcce6d).
func TestReplanFrame_TeachesWiring(t *testing.T) {
	f := replanFrameTemplate
	for _, want := range []string{"${step.0.results.0.url}", "depends_on:[0]", "web_search", "web_fetch", "WIRE your new steps"} {
		if !strings.Contains(f, want) {
			t.Errorf("replan frame no longer teaches wiring — missing %q", want)
		}
	}
	for _, banned := range []string{"Do NOT use `${step", "do not use ${step", "set `depends_on: []`. Do NOT"} {
		if strings.Contains(f, banned) {
			t.Errorf("replan frame re-introduced the anti-wiring ban: %q", banned)
		}
	}
}

// The core EXECUTIVE prompt must keep the wiring mechanics and the concrete
// web_search -> web_fetch chain example. A migration that drops these is the other
// way this regresses.
func TestExecutivePrompt_TeachesStepWiring(t *testing.T) {
	p := prompt.Executive
	for _, want := range []string{
		"## Wiring data between steps",
		"${step.0.results.0.url}", // the placeholder used to wire a fetch to a search result
		"depends_on",
		"web_fetch", // the search -> fetch example
	} {
		if !strings.Contains(p, want) {
			t.Errorf("EXECUTIVE prompt no longer teaches step wiring — missing %q", want)
		}
	}
}

// No bundled skill may tell the planner NOT to wire steps. web_research_guide once
// said "you never plan a separate web_search + web_fetch" / "for everything else use
// web_research", which made every research plan flat. This scans every skill so the
// ban can't creep back in through any of them.
func TestBundledSkills_DoNotBanStepWiring(t *testing.T) {
	files, err := filepath.Glob("../skills/bundled/*/SKILL.md")
	if err != nil {
		t.Fatalf("glob skills: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no bundled skills found at ../skills/bundled/*/SKILL.md — fix the test path")
	}
	banned := []string{
		"never plan a separate web_search",
		"for everything else, use web_research",
		"never plan a separate `web_search`",
	}
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		low := strings.ToLower(string(b))
		for _, phrase := range banned {
			if strings.Contains(low, strings.ToLower(phrase)) {
				t.Errorf("%s bans step wiring — contains %q", filepath.Base(filepath.Dir(path))+"/SKILL.md", phrase)
			}
		}
	}
}

// No bundled skill writes a placeholder the planner would resolve.
//
// A card describes what each step needs; the planner decides how to wire it and
// injects one step's output into another. A card that writes ${step.0.url}
// itself is guessing at a step number it cannot know, and the guess is wrong as
// soon as the planner orders the steps differently — the reference resolves to
// another step's output, or to nothing, and the step runs on the wrong input
// with no error.
//
// skill_creator states the rule and has to name the shape to forbid it, which is
// why this cannot be a plain search for "${step." — that search fails on the
// sentence carrying the rule. What separates them is whether the reference could
// resolve: templates.go matches ${step.<anything>} and ${node.<anything>}, so
// ${step.N.field} and ${node.<id>.field} — N and <id> being stand-ins, not
// values — never resolve to anything, and a card may write those.
func TestBundledSkills_WriteNoResolvablePlaceholder(t *testing.T) {
	files, err := filepath.Glob("../skills/bundled/*/SKILL.md")
	if err != nil {
		t.Fatalf("glob skills: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no bundled skills found at ../skills/bundled/*/SKILL.md — fix the test path")
	}

	// The spellings that name the shape without being one. Anything else the
	// engine's own pattern matches is a reference that would resolve.
	standIns := map[string]bool{
		"${step.N.field}":    true,
		"${node.<id>.field}": true,
		"${step.N.output}":   true,
	}

	found := 0
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		card := filepath.Base(filepath.Dir(path)) + "/SKILL.md"
		for _, ref := range templatePattern.FindAllString(string(b), -1) {
			found++
			if standIns[ref] {
				continue
			}
			t.Errorf("%s writes %s, which the planner would resolve. A card says what "+
				"a step needs; the planner decides which step supplies it, so a "+
				"number written here is a guess that breaks when the plan is ordered "+
				"differently", card, ref)
		}
	}

	// The engine's own pattern is what this test reads with, so if that pattern
	// ever stops matching, this passes by matching nothing. skill_creator carries
	// the rule and names the shape, so there is always at least one to match.
	if found == 0 {
		t.Error("no placeholder-shaped text found in any card, so either skill_creator " +
			"stopped stating the rule or templatePattern no longer matches it")
	}
}
