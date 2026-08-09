package agent

import (
	"encoding/json"
	"testing"

	agenttools "github.com/Compdeep/kaiju/agent/tools"
)

// refAgent registers one tool under the given name with the given output
// schema. A nil schema is a tool that declares nothing about its output.
func refAgent(name string, schema json.RawMessage) *Agent {
	reg := agenttools.NewRegistry()
	reg.Replace(&fakeTool{name: name, params: json.RawMessage(`{}`), output: schema}, "builtin")
	return &Agent{registry: reg}
}

// producedNode adds a resolved tool node carrying the given payload.
func producedNode(g *Graph, tool string, payload map[string]any) string {
	id := g.AddNode(&Node{Type: NodeTool, Tag: tool, ToolName: tool})
	g.SetBody(id, toolMessageBody{msg: agenttools.ToolOK("k", "", payload)})
	return id
}

// A schema listing arrays of objects, with one field marked.
func listSchema(annotation string) json.RawMessage {
	marked := `"url":{"type":"string"}`
	if annotation != "" {
		marked = `"url":{"type":"string","x-reference":"` + annotation + `"}`
	}
	return agenttools.EnvelopeSchema(`{"type":"object","properties":{"results":{"type":"array","items":{"type":"object","properties":{` +
		marked + `,"title":{"type":"string"}}}}}}`)
}

func payloadWith(urls ...string) map[string]any {
	results := make([]map[string]any, 0, len(urls))
	for _, u := range urls {
		results = append(results, map[string]any{"url": u, "title": "t"})
	}
	return map[string]any{"results": results}
}

// ── declared and undeclared ──────────────────────────────────────────────────

func TestReferences_AnnotatedFieldIsFound(t *testing.T) {
	a := refAgent("lister", listSchema("reader.path"))
	g := NewGraph()
	producedNode(g, "lister", payloadWith("a", "b"))

	refs := a.collectReferences(g)
	if len(refs) != 2 {
		t.Fatalf("got %d references, want 2: %+v", len(refs), refs)
	}
	if refs[0].Value != "a" || refs[0].Tool != "reader" || refs[0].Param != "path" {
		t.Fatalf("first reference = %+v, want value a resolved by reader.path", refs[0])
	}
}

// A tool with a perfectly good schema that marks nothing declares nothing. This
// is every tool that has not opted in, so it is the case that must stay silent.
func TestReferences_SchemaWithoutAnnotationFindsNothing(t *testing.T) {
	a := refAgent("lister", listSchema(""))
	g := NewGraph()
	producedNode(g, "lister", payloadWith("a", "b"))

	if refs := a.collectReferences(g); len(refs) != 0 {
		t.Fatalf("an unmarked schema must contribute nothing, got %+v", refs)
	}
}

// A tool that publishes no output schema at all — one of 25 in Kaiju, ten files
// in Enbarr. It must be skipped, not guessed at.
func TestReferences_NoSchemaFindsNothing(t *testing.T) {
	a := refAgent("lister", nil)
	g := NewGraph()
	producedNode(g, "lister", payloadWith("a", "b"))

	if refs := a.collectReferences(g); len(refs) != 0 {
		t.Fatalf("a tool with no schema must contribute nothing, got %+v", refs)
	}
}

// A node whose tool is no longer in the registry: skipped rather than a panic.
func TestReferences_UnregisteredToolFindsNothing(t *testing.T) {
	a := refAgent("lister", listSchema("reader.path"))
	g := NewGraph()
	producedNode(g, "someone_else", payloadWith("a"))

	if refs := a.collectReferences(g); len(refs) != 0 {
		t.Fatalf("an unregistered tool must contribute nothing, got %+v", refs)
	}
}

func TestReferences_NoRegistryOrNoGraph(t *testing.T) {
	g := NewGraph()
	producedNode(g, "lister", payloadWith("a"))
	if refs := (&Agent{}).collectReferences(g); refs != nil {
		t.Errorf("no registry: got %+v", refs)
	}
	if refs := refAgent("lister", listSchema("reader.path")).collectReferences(nil); refs != nil {
		t.Errorf("no graph: got %+v", refs)
	}
}

// ── partial and malformed declarations ───────────────────────────────────────

// An annotation naming a tool but no parameter still marks the field. The
// handle is worth reporting even when nothing said how to follow it.
func TestReferences_AnnotationWithoutParamStillMarks(t *testing.T) {
	a := refAgent("lister", listSchema("reader"))
	g := NewGraph()
	producedNode(g, "lister", payloadWith("a"))

	refs := a.collectReferences(g)
	if len(refs) != 1 || refs[0].Tool != "reader" || refs[0].Param != "" {
		t.Fatalf("got %+v, want the handle marked with a tool and no param", refs)
	}
	// And the floor cannot act on it, so it plans nothing rather than guessing.
	if steps, _ := a.conclusionFloor(g, 5); len(steps) != 0 {
		t.Fatalf("nothing declared how to follow it, so no step should be planned, got %+v", steps)
	}
}

// An empty annotation is still a marking — the field is a handle, nobody said
// what reads it.
func TestReferences_EmptyAnnotationStillMarks(t *testing.T) {
	a := refAgent("lister", agenttools.EnvelopeSchema(`{"type":"object","properties":{"id":{"type":"string","x-reference":""}}}`))
	g := NewGraph()
	producedNode(g, "lister", map[string]any{"id": "alert-7"})

	refs := a.collectReferences(g)
	if len(refs) != 1 || refs[0].Value != "alert-7" || refs[0].Tool != "" {
		t.Fatalf("got %+v, want alert-7 marked with no resolver", refs)
	}
}

func TestReferences_MalformedSchemaIsSkipped(t *testing.T) {
	a := refAgent("lister", json.RawMessage(`{not json`))
	g := NewGraph()
	producedNode(g, "lister", payloadWith("a"))

	if refs := a.collectReferences(g); len(refs) != 0 {
		t.Fatalf("a schema that does not parse must contribute nothing, got %+v", refs)
	}
}

// Declared but absent from this run's payload: nothing to report.
func TestReferences_AnnotatedFieldMissingFromPayload(t *testing.T) {
	a := refAgent("lister", listSchema("reader.path"))
	g := NewGraph()
	producedNode(g, "lister", map[string]any{"results": []map[string]any{{"title": "no url here"}}})

	if refs := a.collectReferences(g); len(refs) != 0 {
		t.Fatalf("got %+v, want nothing", refs)
	}
}

// A marked field that is not inside an array — an id at the top of a payload.
func TestReferences_TopLevelFieldIsFound(t *testing.T) {
	a := refAgent("opener", agenttools.EnvelopeSchema(`{"type":"object","properties":{"incident_id":{"type":"string","x-reference":"investigate.id"}}}`))
	g := NewGraph()
	producedNode(g, "opener", map[string]any{"incident_id": "inc-42"})

	refs := a.collectReferences(g)
	if len(refs) != 1 || refs[0].Value != "inc-42" || refs[0].Tool != "investigate" {
		t.Fatalf("got %+v, want inc-42 resolved by investigate", refs)
	}
}

// ── followed or not ──────────────────────────────────────────────────────────

func TestReferences_FollowedWhenPassedAsAParam(t *testing.T) {
	a := refAgent("lister", listSchema("reader.path"))
	g := NewGraph()
	producedNode(g, "lister", payloadWith("a", "b"))
	g.AddNode(&Node{Type: NodeTool, Tag: "read", ToolName: "reader", Params: map[string]any{"path": "a"}})

	unresolved := a.unresolvedReferences(g)
	if len(unresolved) != 1 || unresolved[0].Value != "b" {
		t.Fatalf("got %+v, want only b outstanding", unresolved)
	}
}

// A handle passed to a step that then failed still counts as followed: the run
// did not overlook it, and the failure is the coverage edge's to report.
func TestReferences_FollowedEvenIfTheStepFailed(t *testing.T) {
	a := refAgent("lister", listSchema("reader.path"))
	g := NewGraph()
	producedNode(g, "lister", payloadWith("a"))
	id := g.AddNode(&Node{Type: NodeTool, Tag: "read", ToolName: "reader", Params: map[string]any{"path": "a"}})
	g.SetError(id, errFailedForTest{})

	if unresolved := a.unresolvedReferences(g); len(unresolved) != 0 {
		t.Fatalf("got %+v, want none — it was passed to something", unresolved)
	}
}

// Nested deep in a parameter object, not at the top: still followed.
func TestReferences_FollowedInsideANestedParam(t *testing.T) {
	a := refAgent("lister", listSchema("reader.path"))
	g := NewGraph()
	producedNode(g, "lister", payloadWith("a"))
	g.AddNode(&Node{Type: NodeTool, Tag: "read", ToolName: "reader",
		Params: map[string]any{"opts": map[string]any{"targets": []any{"a"}}}})

	if unresolved := a.unresolvedReferences(g); len(unresolved) != 0 {
		t.Fatalf("got %+v, want none — the value is in the params, however deep", unresolved)
	}
}

type errFailedForTest struct{}

func (errFailedForTest) Error() string { return "failed" }
