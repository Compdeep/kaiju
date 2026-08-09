package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// A tool's output schema is read by three things that describe the tool to a
// model. All three want the tool's own fields; none wants the envelope's. This
// is the one place that decides which is which, so it is the one place to test.

const searchFields = `{"type":"object","description":"Search results with URLs.","properties":{"query":{"type":"string","description":"the search query executed"},"results":{"type":"array","description":"ranked search results"}}}`

// The wrapped and unwrapped declarations of the same fields must be
// indistinguishable to a reader. This is the assertion the bug lived through:
// EnvelopeSchema moved every tool's fields one level down and two of the three
// readers kept listing the level above.
func TestPayloadSchemaSeesThroughTheEnvelope(t *testing.T) {
	wrapped := PayloadSchema(EnvelopeSchema(searchFields))
	flat := PayloadSchema(json.RawMessage(searchFields))

	for _, got := range []json.RawMessage{wrapped, flat} {
		var s struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(got, &s); err != nil {
			t.Fatalf("not a schema: %v\n%s", err, got)
		}
		if _, ok := s.Properties["query"]; !ok {
			t.Errorf("the tool's own fields are missing: %s", got)
		}
		if _, ok := s.Properties["status"]; ok {
			t.Errorf("the envelope's fields leaked through: %s", got)
		}
	}
}

// A tool that carries only text declares no payload. There is nothing to
// describe, and describing the wrapper instead is what produced five generic
// keys in the planner's prompt.
func TestPayloadSchemaOfATextOnlyToolIsNothing(t *testing.T) {
	if got := PayloadSchema(EnvelopeSchema("")); got != nil {
		t.Errorf("a tool with no payload = %s, want nothing to describe", got)
	}
}

// A schema written by hand around the same shape, with no mark on it. This is
// the rule the chain-hint reader used before the mark existed, and dropping it
// would break any tool whose schema was written that way.
func TestPayloadSchemaSeesAnUnmarkedWrapper(t *testing.T) {
	unmarked := json.RawMessage(`{"type":"object","properties":{"status":{"type":"string"},"data":` + searchFields + `}}`)
	got := PayloadSchema(unmarked)
	if !strings.Contains(string(got), `"results"`) {
		t.Errorf("an unmarked wrapper was not seen through: %s", got)
	}
}

// And a tool that genuinely returns a field called data, which is not a
// wrapper. Treating it as one would hide the rest of that tool's fields.
func TestPayloadSchemaLeavesARealDataFieldAlone(t *testing.T) {
	real := json.RawMessage(`{"type":"object","properties":{"data":{"type":"string","description":"the decoded body"},"encoding":{"type":"string"}}}`)
	got := PayloadSchema(real)
	var s struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(got, &s); err != nil {
		t.Fatalf("not a schema: %v", err)
	}
	if _, ok := s.Properties["encoding"]; !ok {
		t.Errorf("a real data field was mistaken for a wrapper: %s", got)
	}
}

// Every tool built by EnvelopeSchema carries the mark, so a reader never has to
// guess. Without it the only rule available is "does it have a data property",
// which is the guess the case above exists to bound.
func TestEnvelopeSchemaMarksItself(t *testing.T) {
	if !strings.Contains(string(EnvelopeSchema(searchFields)), `"x-envelope":true`) {
		t.Error("EnvelopeSchema does not mark its wrapper")
	}
	if !json.Valid(EnvelopeSchema(searchFields)) {
		t.Error("EnvelopeSchema does not produce valid JSON")
	}
	if !json.Valid(EnvelopeSchema("")) {
		t.Error("EnvelopeSchema(\"\") does not produce valid JSON")
	}
}
