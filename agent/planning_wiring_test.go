package agent

import (
	"github.com/Compdeep/kaiju/agent/gates"
	"io/fs"
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
	// By TAG, not by position. A position is counted from the first step of the
	// plan it is in, so a re-plan renumbers every reference it inherits — which
	// is the one thing a frame written for a re-plan must not teach.
	for _, want := range []string{"${step.find_docs.results.0.url}", "web_search", "web_fetch", "WIRE your new steps"} {
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
		// The reference, and the search → fetch example that uses it. By TAG:
		// it was `${step.0.results.0.url}`, a POSITION, and a position is
		// counted from the first step of the plan it is in, so a plan that
		// drops a step renumbers every reference after it.
		"${step.find_docs.results.0.url}",
		"web_fetch",
		// Still named, because a reference is now the dependency and the
		// prompt has to say when depends_on is still yours to write.
		"depends_on",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("EXECUTIVE prompt no longer teaches step wiring — missing %q", want)
		}
	}
}

// everyShippedCard is every SKILL.md an installation can be given: the ones
// loaded from disk, and the ones inside the binary.
//
// The two tests below scanned only the first set. The second — data_retrieval,
// general_reasoning, self_awareness, system_operations — is compiled in by
// //go:embed and is what an installation with no card directory runs on, so it
// reaches more deployments than the bundled set does and was checked by nothing.
//
// The embedded half is read through the embed.FS rather than off disk, because
// that is the copy that actually ships: a card added to the directory and not
// picked up by the embed pattern is not in the binary, whatever the filesystem
// says.
func everyShippedCard(t *testing.T) map[string]string {
	t.Helper()
	cards := map[string]string{}

	files, err := filepath.Glob("../skills/bundled/*/SKILL.md")
	if err != nil {
		t.Fatalf("glob bundled skills: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no bundled skills found at ../skills/bundled/*/SKILL.md — fix the test path")
	}
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		cards["bundled/"+filepath.Base(filepath.Dir(path))] = string(b)
	}

	embedded, err := fs.Glob(builtinSkillsFS, "prompts/skills/*/SKILL.md")
	if err != nil {
		t.Fatalf("glob embedded skills: %v", err)
	}
	if len(embedded) == 0 {
		t.Fatal("no embedded skills found in builtinSkillsFS — the go:embed pattern no longer reaches them")
	}
	for _, path := range embedded {
		b, err := fs.ReadFile(builtinSkillsFS, path)
		if err != nil {
			t.Fatalf("read embedded %s: %v", path, err)
		}
		cards["embedded/"+filepath.Base(filepath.Dir(path))] = string(b)
	}
	return cards
}

// No shipped skill may tell the planner NOT to wire steps. web_research_guide once
// said "you never plan a separate web_search + web_fetch" / "for everything else use
// web_research", which made every research plan flat. This scans every skill so the
// ban can't creep back in through any of them.
func TestShippedSkills_DoNotBanStepWiring(t *testing.T) {
	banned := []string{
		"never plan a separate web_search",
		"for everything else, use web_research",
		"never plan a separate `web_search`",
	}
	for name, body := range everyShippedCard(t) {
		low := strings.ToLower(body)
		for _, phrase := range banned {
			if strings.Contains(low, strings.ToLower(phrase)) {
				t.Errorf("%s bans step wiring — contains %q", name, phrase)
			}
		}
	}
}

// No shipped skill writes a placeholder the planner would resolve.
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
func TestShippedSkills_WriteNoResolvablePlaceholder(t *testing.T) {
	// The spellings that name the shape without being one. Anything else the
	// engine's own pattern matches is a reference that would resolve.
	standIns := map[string]bool{
		"${step.N.field}":    true,
		"${node.<id>.field}": true,
		"${step.N.output}":   true,
	}

	found := 0
	for card, body := range everyShippedCard(t) {
		for _, ref := range templatePattern.FindAllString(body, -1) {
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

// The frame permits the literal it asks for.
//
// It tells the planner to take a finished step's value out of the tool result
// above and write it into the param. The next sentence used to say "Never paste
// a URL you don't actually have in front of you: a URL to fetch comes from a
// search step, not memory" — which reads as a ban on writing any URL, and the
// planner cannot tell whether a URL sitting in a tool result counts as being in
// front of it.
//
// With that, and with a finished step being unaddressable, and with completed
// steps not to be repeated, a re-plan asked to fetch a URL from an earlier
// search has no move it is allowed to make. Observed: the reflector named the
// exact URL to fetch, and the planner returned no steps and prose telling the
// user to open the site themselves.
func TestReplanFrame_PermitsTheLiteralItAsksFor(t *testing.T) {
	f := replanFrameTemplate

	if strings.Contains(f, "Never paste a URL") {
		t.Error("the blanket ban is back; it forbids the copy the sentence before it instructs")
	}
	if !strings.Contains(f, "take the value out of that and write the value itself into the param") {
		t.Fatal("the frame no longer tells the planner to copy a finished step's value")
	}
	// What replaced it has to keep both halves: copying from the material is
	// allowed, inventing is not. Either alone is a rule that produced a bug.
	for _, want := range []string{
		"you can point to in the material above",
		"a value you recall or assume",
	} {
		if !strings.Contains(f, want) {
			t.Errorf("the rule no longer says %q, so it bans either too much or too little", want)
		}
	}
	// And it must say what to do instead, or a planner that cannot find the
	// value is left with nothing it may do.
	if !strings.Contains(f, "the step that produces it is what to plan instead") {
		t.Error("the rule refuses a value without naming the move that gets one")
	}
}

// The prompt names the intent the way the schema asks for it.
//
// gates.Intent renders as "rank(100)" — it holds a number and says outright
// that naming belongs to the registry. The prompt carried that string while
// the plan schema's intent enum holds the registry's words, so the planner was
// shown a rank and asked for a name with nothing joining the two. Observed: an
// operate-level request came back declaring "observe", and preflight's floor is
// the only reason its tools were not refused.
func TestPlannerIsToldTheIntentByName(t *testing.T) {
	reg := NewIntentRegistry()
	if err := reg.Load(staticIntents{}); err != nil {
		t.Fatal(err)
	}
	for rank, want := range map[int]string{0: "observe", 100: "operate", 200: "override"} {
		if got := intentName(reg, gates.Intent(rank)); got != want {
			t.Errorf("rank %d rendered %q, want %q — the schema's enum holds names", rank, got, want)
		}
	}
	if got := intentName(reg, gates.IntentAuto); got != "auto" {
		t.Errorf("auto rendered %q", got)
	}
	// A rank the registry does not carry falls back rather than inventing a word.
	if got := intentName(reg, gates.Intent(77)); got != "rank(77)" {
		t.Errorf("an unregistered rank rendered %q, want the rank itself", got)
	}
	if got := intentName(nil, gates.Intent(100)); got != "rank(100)" {
		t.Errorf("no registry rendered %q, want the rank itself", got)
	}
}
