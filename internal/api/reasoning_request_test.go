package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// Every per-request field this endpoint accepts must be put ON the trigger.
//
// A field parsed out of the request body and then not carried is the fault the
// config endpoint's own convention test guards against, in the other direction:
// there a setting was stored and never pushed, here one would be validated and
// never used. Both give a caller a clean 200 and no effect.
//
// This reads the handler rather than running it, for the reason the config one
// does: the regression is textual — a field added to the request struct with a
// validation beside it and no assignment.
func TestEveryReasoningRequestFieldReachesTheTrigger(t *testing.T) {
	src, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for field, want := range map[string]string{
		"ReasoningEffort":    "trigger.ReasoningEffort =",
		"ReasoningMaxTokens": "trigger.ReasoningMaxTokens =",
	} {
		if !strings.Contains(body, "req."+field) {
			t.Errorf("%s is never read from the request", field)
			continue
		}
		if !strings.Contains(body, want) {
			t.Errorf("%s is read and validated but never put on the trigger, so a "+
				"caller sending it gets a 200 and no effect", field)
		}
	}
}

// The two fields exist on the request struct with the names the capabilities
// document publishes. A client is written against those names, and a rename
// here would be a silent break: an unknown JSON key decodes to the zero value.
func TestTheRequestStructSpellsThemAsPublished(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "types.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		fld, ok := n.(*ast.Field)
		if !ok || fld.Tag == nil || len(fld.Names) == 0 {
			return true
		}
		found[fld.Names[0].Name] = fld.Tag.Value
		return true
	})
	for field, tag := range map[string]string{
		"ReasoningEffort":    `json:"reasoning_effort,omitempty"`,
		"ReasoningMaxTokens": `json:"reasoning_max_tokens,omitempty"`,
	} {
		got, ok := found[field]
		if !ok {
			t.Errorf("%s is not on the request struct", field)
			continue
		}
		if !strings.Contains(got, tag) {
			t.Errorf("%s is tagged %s, but the capabilities document publishes %s",
				field, got, tag)
		}
	}
}
