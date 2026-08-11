package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/prompt"
)

// The terse persona reaches the stages that answer with a schema.
//
// SOUL_TERSE was declared, validated as a required section and shipped in
// prompts.md, and read by nothing at all. A host could write it and it went
// nowhere, which is indistinguishable from writing it correctly.

func TestTheTersePersonaIsUsedWhereASchemaIsRequired(t *testing.T) {
	schemaStages := []string{
		"executive.go", "microplanner.go", "observer.go",
		"rca.go", "reflection.go", "compute.go",
	}
	for _, f := range schemaStages {
		src := string(readSource(t, f))
		if !strings.Contains(src, "a.terseSoulPrompt") {
			t.Errorf("%s builds its prompt from the full persona; a stage told to "+
				"emit a tool call and nothing else is given paragraphs its "+
				"instructions have to compete with", f)
		}
	}
}

// The stages that write prose keep the full persona: that is the text a reader
// sees the voice of, and shortening it there is a different decision.
func TestTheProseStagesKeepTheFullPersona(t *testing.T) {
	for _, f := range []string{"aggregator.go", "chat.go", "loop_react.go"} {
		src := string(readSource(t, f))
		if !strings.Contains(src, "a.soulPrompt") {
			t.Errorf("%s stopped using the full persona", f)
		}
	}
}

// A host with no shorter wording gets the full one, which is what makes the
// section optional rather than required-and-ignored.
func TestTheTersePersonaFallsBackToTheFullOne(t *testing.T) {
	saved := prompt.SoulTerse
	t.Cleanup(func() { prompt.SoulTerse = saved })

	prompt.SoulTerse = "   "
	if got := terseSoul("the whole thing"); got != "the whole thing" {
		t.Errorf("with no terse section the persona was %q, want the full one", got)
	}
	prompt.SoulTerse = "short"
	if got := terseSoul("the whole thing"); got != "short" {
		t.Errorf("the terse section was declared and ignored, got %q", got)
	}
}
