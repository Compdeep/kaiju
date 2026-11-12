package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A reference to a field the earlier step did not return.
//
// The resolver used to fall back to the earlier step's whole text whenever the
// field was missing, and a typed tool's result is always text. So an empty
// web_search handed its message, "(no search: no reachable results …)", to
// web_fetch as the URL, and the run showed "invalid URL" instead of "the search
// found nothing" (runs d9d652e3 and b09e2b84, three times each).

// refTool declares which of its parameters are required.
type refTool struct {
	name   string
	schema string
}

func (r *refTool) Name() string                { return r.name }
func (r *refTool) Description() string         { return "" }
func (r *refTool) Parameters() json.RawMessage { return json.RawMessage(r.schema) }
func (r *refTool) Impact(map[string]any) int   { return toolapi.ImpactObserve }
func (r *refTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

func missingFieldSetup(t *testing.T, msg toolapi.ToolMessage) (*Graph, *toolapi.Registry, string) {
	t.Helper()
	reg := toolapi.NewRegistry()
	reg.Replace(&refTool{name: "fetch", schema: `{"type":"object","properties":{"url":{"type":"string"},"focus":{"type":"string"}},"required":["url"]}`}, "builtin")
	reg.Replace(&refTool{name: "calc", schema: `{"type":"object","properties":{"goal":{"type":"string"},"context":{"type":"array","items":{"type":"string"}}},"required":["goal"]}`}, "builtin")
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "search_helio", ToolName: "web_search"})
	g.SetBody(id, toolMessageBody{msg: msg})
	return g, reg, id
}

var emptySearch = toolapi.ToolEmpty("search", "no reachable results for this query")

// Required parameter, earlier step empty: the step is skipped, and the reason
// names the step that found nothing.
func TestMissingFieldFromAnEmptyStepBlocksARequiredParameter(t *testing.T) {
	g, reg, dep := missingFieldSetup(t, emptySearch)
	n := &Node{ToolName: "fetch", Params: map[string]any{"url": "${node." + dep + ".results.0.url}"}}
	n.ID = g.AddNode(n)

	err := substituteTemplates(n, g, reg)
	var blocked *blockedByDep
	if !errors.As(err, &blocked) {
		t.Fatalf("error = %v (%T), want the step blocked", err, err)
	}
	if !strings.Contains(err.Error(), "search_helio") || !strings.Contains(err.Error(), "no reachable results") {
		t.Errorf("error %q should name the step and what it reported", err)
	}
	if url, _ := n.Params["url"].(string); strings.Contains(url, "no reachable") {
		t.Errorf("the empty step's message reached the parameter: %q", url)
	}
}

// Same for an earlier step that returned an error.
func TestMissingFieldFromAFailedStepBlocksARequiredParameter(t *testing.T) {
	g, reg, dep := missingFieldSetup(t, toolapi.ToolFail("search", "the search service refused the request", nil))
	n := &Node{ToolName: "fetch", Params: map[string]any{"url": "${node." + dep + ".results.0.url}"}}
	n.ID = g.AddNode(n)

	var blocked *blockedByDep
	if err := substituteTemplates(n, g, reg); !errors.As(err, &blocked) {
		t.Fatalf("error = %v (%T), want the step blocked", err, err)
	}
}

// Earlier step ok, field missing: the plan named a field the tool did not
// return. That is the step's own failure, not a skip.
func TestMissingFieldFromAnOKStepFailsTheStep(t *testing.T) {
	g, reg, dep := missingFieldSetup(t, toolapi.ToolOK("search", "", map[string]any{"query": "q", "results": []any{}}))
	n := &Node{ToolName: "fetch", Params: map[string]any{"url": "${node." + dep + ".hits.0.link}"}}
	n.ID = g.AddNode(n)

	err := substituteTemplates(n, g, reg)
	if err == nil {
		t.Fatal("a reference to a field the step did not return was accepted")
	}
	var blocked *blockedByDep
	if errors.As(err, &blocked) {
		t.Errorf("error = %v, want a failure of this step, not a skip", err)
	}
	if !strings.Contains(err.Error(), "absent") {
		t.Errorf("error %q should say the field is absent", err)
	}
}

// Optional parameter: the step runs. A "name=value" entry keeps its name and
// says why the value is missing; the other entries are untouched.
func TestMissingFieldInAnOptionalListEntryRunsWithANote(t *testing.T) {
	g, reg, dep := missingFieldSetup(t, emptySearch)
	other := g.AddNode(&Node{Type: NodeTool, Tag: "fetch_umbriel", ToolName: "fetch"})
	g.SetBody(other, toolMessageBody{msg: toolapi.ToolOK("page", "longitudes", map[string]any{"content": "longitudes"})})
	n := &Node{ToolName: "calc", Params: map[string]any{
		"goal": "barycenter",
		"context": []any{
			"helio_data=${node." + dep + ".results.0.url}",
			"umbriel_data=${node." + other + ".content}",
		},
	}}
	n.ID = g.AddNode(n)

	if err := substituteTemplates(n, g, reg); err != nil {
		t.Fatalf("an optional input that is missing stopped the step: %v", err)
	}
	ctx, _ := n.Params["context"].([]any)
	if len(ctx) != 2 {
		t.Fatalf("context = %#v, want both entries", ctx)
	}
	first, _ := ctx[0].(string)
	if !strings.HasPrefix(first, "helio_data=") || !strings.Contains(first, "not available") || !strings.Contains(first, "search_helio") {
		t.Errorf("the missing entry reads %q, want its name and why it is not available", first)
	}
	if ctx[1] != "umbriel_data=longitudes" {
		t.Errorf("the other entry = %v, want it injected as usual", ctx[1])
	}
}

// An optional parameter holding only the reference is left out.
func TestMissingFieldInAnOptionalParameterDropsIt(t *testing.T) {
	g, reg, dep := missingFieldSetup(t, emptySearch)
	n := &Node{ToolName: "fetch", Params: map[string]any{
		"url":   "https://example.com/",
		"focus": "${node." + dep + ".results.0.title}",
	}}
	n.ID = g.AddNode(n)

	if err := substituteTemplates(n, g, reg); err != nil {
		t.Fatalf("an optional input that is missing stopped the step: %v", err)
	}
	if _, ok := n.Params["focus"]; ok {
		t.Errorf("focus = %v, want it left out", n.Params["focus"])
	}
}

// A plain-text tool has no envelope to tell empty from ok: its whole text is
// still what a reference into it gets.
func TestMissingFieldFromUnclassifiedTextKeepsTheWholeText(t *testing.T) {
	g, reg, dep := missingFieldSetup(t, toolapi.ToolMessage{Type: "text", Status: toolapi.StatusUnclassified, Content: "plain words"})
	n := &Node{ToolName: "fetch", Params: map[string]any{"url": "${node." + dep + ".anything}"}}
	n.ID = g.AddNode(n)

	if err := substituteTemplates(n, g, reg); err != nil {
		t.Fatalf("plain text from an unclassified tool was refused: %v", err)
	}
	if n.Params["url"] != "plain words" {
		t.Errorf("url = %v, want the tool's whole text", n.Params["url"])
	}
}
