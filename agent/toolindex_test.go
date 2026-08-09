package agent

import (
	"encoding/json"
	"strings"
	"testing"

	agenttools "github.com/Compdeep/kaiju/agent/tools"
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

// Declaring the same fields either way must reach the planner identically.
func TestToolIndexShowsTheToolNotTheEnvelope(t *testing.T) {
	wrapped := compactOutputShape(agenttools.EnvelopeSchema(indexFields))
	flat := compactOutputShape(json.RawMessage(indexFields))

	if wrapped != flat {
		t.Errorf("the same fields render differently by declaration:\n  wrapped: %s\n  flat:    %s", wrapped, flat)
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
		agenttools.EnvelopeSchema(indexFields),
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
