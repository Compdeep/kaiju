package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// What a planner is told a tool returns.
//
// It has to be the tool's own fields. The envelope is plumbing — a status, a
// detail, a payload wrapper — and every tool has the same one, so a planner
// shown the envelope is shown nothing that distinguishes this tool from any
// other and cannot write a ${step.N.field} reference into it.
//
// This is the assertion the bug lived through. EnvelopeSchema moved every
// tool's fields one level down on 2 August; the chain-hint reader was written
// to descend and the other two were not, so for months the planner prompt
// carried {content, data, detail, status, type} for fifteen tools.

const indexFields = `{"type":"object","properties":{"query":{"type":"string","description":"the search query executed"},"results":{"type":"array","description":"ranked search results"}}}`

// Declaring the same fields either way reaches the planner identically, except
// that an envelope also carries content — the field holding the text a tool
// returned. That one is the tool's value, not its plumbing: a read whose payload
// is only path, lines_shown and truncated has its document in content, and a
// planner never shown the name writes ${step.tag} for "the whole result" and
// receives the measurements. So content is expected here, and nothing else from
// the envelope is.
func TestToolIndexShowsTheToolNotTheEnvelope(t *testing.T) {
	wrapped := compactOutputShape(toolapi.EnvelopeSchema(indexFields))
	flat := compactOutputShape(json.RawMessage(indexFields))

	if !strings.Contains(wrapped, "content:") {
		t.Errorf("an envelope must name content, the field its text arrives in: %s", wrapped)
	}
	if strings.Contains(flat, "content") {
		t.Errorf("a flat schema declares no content, so none should be rendered: %s", flat)
	}
	// Identical once content is set aside: the envelope contributes that field
	// and nothing else.
	if stripped := removeField(wrapped, "content"); stripped != flat {
		t.Errorf("the envelope contributed more than content:\n  wrapped minus content: %s\n  flat:                  %s", stripped, flat)
	}
	for _, want := range []string{"query", "results"} {
		if !strings.Contains(wrapped, want) {
			t.Errorf("the planner is not shown %q: %s", want, wrapped)
		}
	}
	for _, leak := range []string{"status", "detail", "Uniform tool envelope"} {
		if strings.Contains(wrapped, leak) {
			t.Errorf("the envelope leaked into the planner prompt (%q): %s", leak, wrapped)
		}
	}
}

// The same schema, checked the other way round: a reference to a real field
// must pass plan-time validation. It used to warn on every correct one.
func TestAReferenceToARealFieldValidates(t *testing.T) {
	for _, schema := range []json.RawMessage{
		toolapi.EnvelopeSchema(indexFields),
		json.RawMessage(indexFields),
	} {
		if !fieldExistsInSchema(schema, "results.0.url") && !fieldExistsInSchema(schema, "results") {
			t.Errorf("a reference to a declared field does not validate: %s", schema)
		}
		if fieldExistsInSchema(schema, "no_such_field") {
			t.Errorf("a reference to a field that does not exist validated: %s", schema)
		}
	}
}

// The sweep over every tool lives in internal/tools, which is the only package
// that can see them: agent cannot import it without a cycle. See
// schema_contract_test.go there.

// The builtins that live in this package rather than internal/tools, so the
// sweep there cannot see them. Same property: a tool must say what it returns,
// and must not describe itself as the envelope it comes in.
func TestTheAgentBuiltinsDeclareWhatTheyReturn(t *testing.T) {
	for _, tool := range []toolapi.Tool{
		&ComputeTool{}, &EditFileTool{}, &DebugTool{}, &VisionTool{},
	} {
		t.Run(tool.Name(), func(t *testing.T) {
			schema := toolapi.GetOutputSchema(tool)
			if schema == nil {
				t.Fatal("declares no output schema — a planner can call it and never chain it")
			}
			if !json.Valid(schema) {
				t.Fatalf("output schema is not valid JSON:\n%s", schema)
			}
			if shape := compactOutputShape(schema); strings.Contains(shape, "Uniform tool envelope") {
				t.Errorf("describes itself to the planner as the envelope: %s", shape)
			} else if shape == "" {
				t.Error("renders as nothing in the planner's tool index")
			}
			if _, ok := tool.(toolapi.TypedExecutor); !ok {
				t.Error("does not return a ToolMessage, so its outcome never reaches the coverage statement")
			}
		})
	}
}

// removeField drops one "name: description" entry from a rendered shape, so the
// rest can be compared with a shape that never had it.
func removeField(shape, name string) string {
	inner := strings.TrimSuffix(strings.TrimPrefix(shape, "{"), "}")
	kept := []string{}
	for _, part := range strings.Split(inner, ", ") {
		if strings.HasPrefix(part, name+":") {
			continue
		}
		kept = append(kept, part)
	}
	return "{" + strings.Join(kept, ", ") + "}"
}
