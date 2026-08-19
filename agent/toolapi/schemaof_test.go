package toolapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The declaration a tool publishes matches the payload it marshals.
//
// This is the whole point of deriving one from the other: a hand-written schema
// beside a struct agrees with it on the day it is written. So the test marshals a
// filled-in value and checks that every key JSON produced is a key the derived
// schema declares, and the other way round.
func TestDerivedSchemaNamesExactlyWhatJSONProduces(t *testing.T) {
	type inner struct {
		Port int    `json:"port"`
		Name string `json:"name"`
	}
	type payload struct {
		Count    int               `json:"count" desc:"how many were found"`
		Host     string            `json:"host,omitempty"`
		Reach    bool              `json:"reachable"`
		Latency  float64           `json:"latency_ms"`
		Rows     []inner           `json:"rows"`
		Labels   map[string]string `json:"labels"`
		Seen     time.Time         `json:"seen"`
		Raw      json.RawMessage   `json:"raw"`
		Skipped  string            `json:"-"`
		internal string
	}

	value := payload{
		Count: 2, Host: "h", Reach: true, Latency: 1.5,
		Rows:   []inner{{Port: 22, Name: "ssh"}},
		Labels: map[string]string{"a": "b"},
		Raw:    json.RawMessage(`{}`),
		// Skipped and internal are set to prove they stay out of both sides.
		Skipped: "x", internal: "y",
	}
	_ = value.internal

	marshalled, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var produced map[string]json.RawMessage
	if err := json.Unmarshal(marshalled, &produced); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	declared, ok := DeclaredPayloadFields(EnvelopeSchema(PayloadSchemaOf(payload{})))
	if !ok {
		t.Fatal("the derived schema declares no payload at all")
	}
	declaredSet := map[string]bool{}
	for _, name := range declared {
		declaredSet[name] = true
	}

	for name := range produced {
		if !declaredSet[name] {
			t.Errorf("json produces %q and the declaration does not name it", name)
		}
	}
	for _, name := range declared {
		if _, found := produced[name]; !found {
			t.Errorf("the declaration names %q and json does not produce it", name)
		}
	}
	if declaredSet["Skipped"] || declaredSet["-"] || declaredSet["internal"] {
		t.Error("a field json skips is declared")
	}
}

// The rendered schema parses, and says the types it should.
func TestDerivedSchemaIsWellFormed(t *testing.T) {
	type inner struct {
		Port int `json:"port"`
	}
	type payload struct {
		Text  string    `json:"text"`
		Num   int       `json:"num"`
		Frac  float64   `json:"frac"`
		Flag  bool      `json:"flag"`
		Rows  []inner   `json:"rows"`
		Bytes []byte    `json:"bytes"`
		Any   any       `json:"any"`
		When  time.Time `json:"when"`
	}
	schema := PayloadSchemaOf(payload{})
	if err := schemaMustParse(schema); err != nil {
		t.Fatalf("%v: %s", err, schema)
	}
	for _, want := range []string{
		`"text":{"type":"string"}`,
		`"num":{"type":"integer"}`,
		`"frac":{"type":"number"}`,
		`"flag":{"type":"boolean"}`,
		`"rows":{"type":"array","items":{"type":"object","properties":{"port":{"type":"integer"}}}}`,
		`"bytes":{"type":"string","contentEncoding":"base64"}`,
		`"any":{}`,
		`"when":{"type":"string","format":"date-time"}`,
	} {
		if !strings.Contains(schema, want) {
			t.Errorf("missing %s\nin %s", want, schema)
		}
	}
}

// A field's desc tag reaches the declaration, and the result still parses.
func TestDescriptionTagSurvives(t *testing.T) {
	type payload struct {
		Count int `json:"count" desc:"listeners found, counted before the text was cut"`
		Any   any `json:"any" desc:"whatever the tool put here"`
	}
	schema := PayloadSchemaOf(payload{})
	if err := schemaMustParse(schema); err != nil {
		t.Fatalf("%v: %s", err, schema)
	}
	if !strings.Contains(schema, "counted before the text was cut") {
		t.Errorf("the description is not in the schema: %s", schema)
	}
	// An empty schema with a description added is the case that produced invalid
	// JSON before: {"description":"...",}
	if !strings.Contains(schema, `"any":{"description":"whatever the tool put here"}`) {
		t.Errorf("a description on an untyped field is wrong: %s", schema)
	}
}

// An embedded struct's fields land where json puts them.
func TestEmbeddedFieldsArePromoted(t *testing.T) {
	type common struct {
		Action string `json:"action"`
	}
	type payload struct {
		common
		Count int `json:"count"`
	}
	declared, ok := DeclaredPayloadFields(EnvelopeSchema(PayloadSchemaOf(payload{})))
	if !ok {
		t.Fatal("no payload declared")
	}
	got := strings.Join(declared, ",")
	if got != "action,count" {
		t.Errorf("declared %q, want action,count", got)
	}
}

// The same struct renders to the same bytes every time.
//
// Map order would otherwise make one binary's declaration differ between runs,
// which reads as a change wherever schemas are compared.
func TestRenderingIsStable(t *testing.T) {
	type payload struct {
		A string `json:"a"`
		B string `json:"b"`
		C string `json:"c"`
		D string `json:"d"`
		E string `json:"e"`
	}
	first := PayloadSchemaOf(payload{})
	for i := 0; i < 20; i++ {
		if again := PayloadSchemaOf(payload{}); again != first {
			t.Fatalf("run %d differs:\n%s\n%s", i, first, again)
		}
	}
}

// A type that contains itself terminates.
func TestSelfReferentialTypeTerminates(t *testing.T) {
	type node struct {
		Name     string  `json:"name"`
		Children []*node `json:"children"`
	}
	schema := PayloadSchemaOf(node{})
	if err := schemaMustParse(schema); err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(schema, `"name"`) {
		t.Errorf("gave up before declaring anything: %s", schema)
	}
}

// Anything that is not a struct declares no fields rather than a broken schema.
func TestNonStructDeclaresNothingUsable(t *testing.T) {
	for _, v := range []any{nil, "text", 42, []string{"a"}, map[string]any{"a": 1}} {
		schema := PayloadSchemaOf(v)
		if err := schemaMustParse(schema); err != nil {
			t.Errorf("%T produced a schema that does not parse: %v", v, err)
		}
		if _, ok := DeclaredPayloadFields(EnvelopeSchema(schema)); ok {
			t.Errorf("%T declared payload fields", v)
		}
	}
}

// A pointer to a struct declares the same as the struct.
func TestPointerRendersAsItsStruct(t *testing.T) {
	type payload struct {
		Count int `json:"count"`
	}
	if PayloadSchemaOf(&payload{}) != PayloadSchemaOf(payload{}) {
		t.Error("a pointer and its struct declare different things")
	}
}

// The note is for a payload whose fields depend on the action asked for.
func TestNoteReachesThePayloadDescription(t *testing.T) {
	type payload struct {
		Action string `json:"action"`
	}
	schema := PayloadSchemaOfWithNote(payload{}, "fields depend on action")
	if err := schemaMustParse(schema); err != nil {
		t.Fatalf("%v: %s", err, schema)
	}
	if !strings.Contains(schema, "fields depend on action") {
		t.Errorf("the note is missing: %s", schema)
	}
	if declared, ok := DeclaredPayloadFields(EnvelopeSchema(schema)); !ok || len(declared) != 1 {
		t.Errorf("the note cost the fields: %v", declared)
	}
}

// A payload may have a field called content or status, and they are the tool's.
//
// The envelope's four framing names are stripped from a schema written flat, where
// they belong to the wrapper. Stripping them inside a data object was wrong: a
// web_fetch payload carries the page's text and the HTTP status line, and removing
// them made a tool that declares them correctly read as one returning undeclared
// fields — which is how this was found, in a table comparing every tool's produced
// keys against its declaration.
func TestFramingNamesAreOnlyStrippedFromAFlatSchema(t *testing.T) {
	wrapped := EnvelopeSchema(`{"type":"object","properties":{` +
		`"content":{"type":"string"},"status":{"type":"string"},"title":{"type":"string"}}}`)
	fields, ok := DeclaredPayloadFields(wrapped)
	if !ok {
		t.Fatal("a marked envelope declared no payload")
	}
	got := strings.Join(fields, ",")
	if got != "content,status,title" {
		t.Errorf("declared %q — a payload's own content and status must survive", got)
	}

	// Written flat, the same two names are the envelope's and are not the tool's.
	flat := json.RawMessage(`{"type":"object","properties":{` +
		`"content":{"type":"string"},"count":{"type":"integer"}}}`)
	fields, ok = DeclaredPayloadFields(flat)
	if !ok {
		t.Fatal("a flat schema declared no payload")
	}
	if strings.Join(fields, ",") != "count" {
		t.Errorf("declared %v — the envelope's content is not a field of the tool", fields)
	}
}
