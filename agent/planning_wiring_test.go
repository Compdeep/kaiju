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
