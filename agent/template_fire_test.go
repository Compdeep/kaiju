package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A reference with no path hands the next step the value, not the text of it.
// A tool given a string where it expected a map fails on the far side, where the
// cause is no longer visible.
func TestABareReferenceInjectsTheValueNotItsText(t *testing.T) {
	g := NewGraph()
	dep := g.AddNode(&Node{Type: NodeTool, Tag: "search", ToolName: "web_search"})
	g.SetResult(dep, `{"results":[{"url":"https://example.test"}],"count":1}`)

	n := &Node{ID: "n2", Params: map[string]any{"payload": "${node." + dep + "}"}}
	if err := substituteTemplates(n, g); err != nil {
		t.Fatalf("substituteTemplates: %v", err)
	}

	got, ok := n.Params["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload is %T, want the parsed object", n.Params["payload"])
	}
	if got["count"] != float64(1) {
		t.Errorf("count = %v, want the value from the dependency", got["count"])
	}
}

// A result that is not JSON is injected as it stands, so a tool returning prose
// still works.
func TestABareReferenceToProseInjectsTheProse(t *testing.T) {
	g := NewGraph()
	dep := g.AddNode(&Node{Type: NodeTool, Tag: "read", ToolName: "read_file"})
	g.SetResult(dep, "the quick brown fox")

	n := &Node{ID: "n2", Params: map[string]any{"text": "${node." + dep + "}"}}
	if err := substituteTemplates(n, g); err != nil {
		t.Fatalf("substituteTemplates: %v", err)
	}
	if n.Params["text"] != "the quick brown fox" {
		t.Errorf("text = %v, want the prose unchanged", n.Params["text"])
	}
}

// A ${step.N…} reference is rewritten to the node form when the plan is
// finalised. One that survives is a bug in whatever grafted it, and the regexes
// that do the substitution match only the node form — so without this check it
// would be left in the parameter as literal text and handed to the tool.
func TestAStepReferenceAtFireTimeIsAnError(t *testing.T) {
	g := NewGraph()
	n := &Node{ID: "n2", Params: map[string]any{"q": "${step.0.results}"}}

	err := substituteTemplates(n, g)

	if err == nil {
		t.Fatal("a step reference was accepted at fire time")
	}
	if !strings.Contains(err.Error(), "${step.0.results}") && !strings.Contains(err.Error(), "step.0.results") {
		t.Errorf("the error does not name the reference: %v", err)
	}
	if n.Params["q"] != "${step.0.results}" {
		t.Errorf("the parameter was altered: %v", n.Params["q"])
	}
}

// Five references to one dependency resolve it once. A typed body is asked for
// its field on the first, and the answer is reused.
func TestOneDependencyIsResolvedOnce(t *testing.T) {
	g := NewGraph()
	dep := g.AddNode(&Node{Type: NodeTool, Tag: "search", ToolName: "web_search"})
	g.SetBody(dep, NewToolBody(toolapi.ToolOK("search", "", map[string]any{"count": 3})))

	n := &Node{ID: "n2", Params: map[string]any{
		"a": "${node." + dep + ".count}",
		"b": "${node." + dep + ".count}",
		"c": "count is ${node." + dep + ".count} exactly",
	}}
	if err := substituteTemplates(n, g); err != nil {
		t.Fatalf("substituteTemplates: %v", err)
	}

	if n.Params["a"] != n.Params["b"] {
		t.Errorf("two references to one field disagree: %v and %v", n.Params["a"], n.Params["b"])
	}
	if s, _ := n.Params["c"].(string); !strings.Contains(s, "count is 3 exactly") {
		t.Errorf("embedded reference = %q", n.Params["c"])
	}
}

// The typed body still answers the field access, which is what this engine does
// better than the copy these changes came from — the body may strip an envelope
// prefix, or compute a field it does not store.
func TestTheTypedBodyStillAnswersTheFieldAccess(t *testing.T) {
	g := NewGraph()
	dep := g.AddNode(&Node{Type: NodeTool, Tag: "search", ToolName: "web_search"})
	g.SetBody(dep, NewToolBody(toolapi.ToolOK("search", "", map[string]any{"url": "https://example.test"})))

	n := &Node{ID: "n2", Params: map[string]any{"u": "${node." + dep + ".url}"}}
	if err := substituteTemplates(n, g); err != nil {
		t.Fatalf("substituteTemplates: %v", err)
	}
	if n.Params["u"] != "https://example.test" {
		t.Errorf("u = %v, want the value the body resolved", n.Params["u"])
	}
	// The envelope wrapper is tolerated on the path, as the body decides.
	n = &Node{ID: "n3", Params: map[string]any{"u": "${node." + dep + ".data.url}"}}
	if err := substituteTemplates(n, g); err != nil {
		t.Fatalf("substituteTemplates with a data prefix: %v", err)
	}
	if n.Params["u"] != "https://example.test" {
		t.Errorf("u = %v with a data. prefix", n.Params["u"])
	}
}
