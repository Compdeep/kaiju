package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// A parameter that takes one of a fixed set of words was rendered as a bare
// name, so the planner had to invent the word. Measured on a live node:
// network_diag(target, action*) was shown, the planner wrote
// {"action":"connectivity"} on three steps of one plan, every one rejected at
// run time with "unknown action", and that run gathered no evidence at all.
//
// The values are in the tool's own schema. This asserts they reach the line the
// planner reads.
func TestSignatureCarriesTheValuesAParameterAccepts(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["ping", "resolve", "sockets", "interfaces"]},
			"target": {"type": "string"}
		},
		"required": ["action"]
	}`)

	got := compactParamSignature(schema)
	want := "action*: ping|resolve|sockets|interfaces, target"
	if got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
}

// Every value or none: a value left out of the line is a value the planner has
// to guess at, which is the condition this exists to end.
func TestSignatureElidesNoValue(t *testing.T) {
	var props []string
	var vals []string
	for i := 0; i < 24; i++ {
		vals = append(vals, "v"+string(rune('a'+i%26))+string(rune('0'+i/10)))
	}
	props = append(props, `"mode": {"type":"string","enum":["`+strings.Join(vals, `","`)+`"]}`)
	schema := json.RawMessage(`{"type":"object","properties":{` + strings.Join(props, ",") + `}}`)

	got := compactParamSignature(schema)
	for _, v := range vals {
		if !strings.Contains(got, v) {
			t.Fatalf("value %q was dropped from the signature: %s", v, got)
		}
	}
}

// A parameter with no fixed set is unchanged — the signature must not grow a
// colon for every free-text parameter, and the tool index is already the
// largest section of the planner's prompt.
func TestSignatureUnchangedWithoutAFixedSet(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"query": {"type": "string"}, "max_results": {"type": "integer"}},
		"required": ["query"]
	}`)
	if got, want := compactParamSignature(schema), "max_results, query*"; got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
}

// Numbers and booleans are written as the schema holds them, so the planner
// copies back a form the dispatcher accepts.
func TestSignatureWritesNonStringValuesAsTheSchemaHoldsThem(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"depth": {"type": "integer", "enum": [1, 2, 3]}, "deep": {"type": "boolean", "enum": [true, false]}}
	}`)
	got := compactParamSignature(schema)
	if !strings.Contains(got, "depth: 1|2|3") || !strings.Contains(got, "deep: true|false") {
		t.Errorf("signature = %q", got)
	}
}

// The same schema rendered twice must produce the same line: Go map order is
// not stable, and a signature that reorders between runs changes the prompt for
// no reason.
func TestSignatureIsStableAcrossRenders(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"zulu": {"type": "string"}, "alpha": {"type": "string"}, "mike": {"type": "string"}},
		"required": ["mike"]
	}`)
	first := compactParamSignature(schema)
	for i := 0; i < 50; i++ {
		if got := compactParamSignature(schema); got != first {
			t.Fatalf("render %d = %q, first = %q", i, got, first)
		}
	}
}
