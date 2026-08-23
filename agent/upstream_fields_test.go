package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// schemaTool declares an output schema. The field names are deliberately not
// any real tool's: if this ever starts working because the engine recognises a
// name, these tests keep passing while production does not.
type schemaTool struct {
	name string
	out  json.RawMessage
}

func (s *schemaTool) Name() string                { return s.name }
func (s *schemaTool) Description() string         { return "" }
func (s *schemaTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (s *schemaTool) Impact(map[string]any) int   { return 0 }
func (s *schemaTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}
func (s *schemaTool) OutputSchema() json.RawMessage { return s.out }

var _ toolapi.Tool = (*schemaTool)(nil)
var _ toolapi.Outputter = (*schemaTool)(nil)

const (
	partDesc  = "only the opening of it, cut down to fit a prompt"
	wholeDesc = "the file holding all of it, not a cut-down copy"
)

func fieldMeaningsFixture(t *testing.T, declaresSchema bool) (*Agent, *Graph, *Node) {
	t.Helper()
	reg := toolapi.NewRegistry()
	tool := &schemaTool{name: "reader"}
	if declaresSchema {
		tool.out = toolapi.EnvelopeSchema(`{"type":"object","properties":{
			"part":{"type":"string","description":"` + partDesc + `"},
			"everything":{"type":"string","description":"` + wholeDesc + `"},
			"unused":{"type":"string","description":"never wired anywhere"}
		}}`)
	}
	reg.Replace(tool, "builtin")

	g := NewGraph()
	producer := g.AddNode(&Node{Type: NodeTool, Tag: "read", ToolName: "reader"})
	consumer := &Node{Type: NodeCompute, Tag: "work", DependsOn: []string{producer}}
	g.AddNode(consumer)
	return &Agent{registry: reg}, g, consumer
}

func TestUpstreamFieldMeanings_DescribesOnlyTheFieldsActuallyPresent(t *testing.T) {
	a, g, n := fieldMeaningsFixture(t, true)
	ctx := map[string]any{"text": map[string]any{"part": "the opening", "everything": "kept/doc.txt"}}

	got := a.upstreamFieldMeanings(g, n, ctx)

	if !strings.Contains(got, partDesc) || !strings.Contains(got, wholeDesc) {
		t.Fatalf("both wired fields must be described, got:\n%s", got)
	}
	if strings.Contains(got, "never wired anywhere") {
		t.Fatalf("a field nobody wired must not be described, got:\n%s", got)
	}
	if !strings.Contains(got, "reader") {
		t.Fatalf("the producing tool must be named, got:\n%s", got)
	}
}

// Nested wherever the planner put it — the key is what matches, not its depth.
func TestUpstreamFieldMeanings_FindsKeysAtAnyDepth(t *testing.T) {
	a, g, n := fieldMeaningsFixture(t, true)
	ctx := map[string]any{"a": []any{map[string]any{"b": map[string]any{"everything": "kept/doc.txt"}}}}

	if got := a.upstreamFieldMeanings(g, n, ctx); !strings.Contains(got, wholeDesc) {
		t.Fatalf("a key nested in a list must still be described, got:\n%s", got)
	}
}

// A tool that declares no output schema is not described, rather than guessed at.
func TestUpstreamFieldMeanings_SilentWithoutADeclaration(t *testing.T) {
	a, g, n := fieldMeaningsFixture(t, false)
	ctx := map[string]any{"part": "the opening", "everything": "kept/doc.txt"}

	if got := a.upstreamFieldMeanings(g, n, ctx); got != "" {
		t.Fatalf("no schema means nothing to say, got:\n%s", got)
	}
}

// Only the steps this one depends on are consulted. Another step's tool
// describing a field of the same name says nothing about this one's values.
func TestUpstreamFieldMeanings_OnlyConsultsItsOwnDependencies(t *testing.T) {
	a, g, n := fieldMeaningsFixture(t, true)
	n.DependsOn = nil

	if got := a.upstreamFieldMeanings(g, n, map[string]any{"part": "x"}); got != "" {
		t.Fatalf("with no dependencies there is nothing to describe, got:\n%s", got)
	}
}

func TestUpstreamFieldMeanings_SilentWhenItCannotTell(t *testing.T) {
	a, g, n := fieldMeaningsFixture(t, true)
	for _, c := range []struct {
		name string
		got  string
	}{
		{"no context", a.upstreamFieldMeanings(g, n, nil)},
		{"context with no keys", a.upstreamFieldMeanings(g, n, "just a string")},
		{"no graph", a.upstreamFieldMeanings(nil, n, map[string]any{"part": "x"})},
		{"no node", a.upstreamFieldMeanings(g, nil, map[string]any{"part": "x"})},
	} {
		if c.got != "" {
			t.Fatalf("%s must describe nothing, got:\n%s", c.name, c.got)
		}
	}
}
