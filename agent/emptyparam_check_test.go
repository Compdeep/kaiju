package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// mirrors tools.FileWrite.Parameters(); tools imports agent, so it cannot be
// imported here. tools/file_write_schema_test.go guards the two staying equal.
const fileWriteSchema = `{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Path to write to"},
		"content": {"type": "string", "minLength": 0, "description": "Content to write. May be empty: \"\" creates an empty file, and truncates an existing one."},
		"append": {"type": "boolean", "description": "Append instead of overwrite (default: false)"}
	},
	"required": ["path", "content"],
	"additionalProperties": false
}`

func TestEmptyStringSuppliedWhenSchemaAllows(t *testing.T) {
	schema, err := parseToolSchema(json.RawMessage(fileWriteSchema))
	if err != nil {
		t.Fatalf("schema unreadable: %v", err)
	}
	cases := []struct {
		name    string
		params  map[string]any
		wantErr string
	}{
		{"empty content creates an empty file", map[string]any{"path": "/tmp/xyz", "content": ""}, ""},
		{"a lone newline is real content", map[string]any{"path": "/tmp/xyz", "content": "\n"}, ""},
		{"ordinary content", map[string]any{"path": "/tmp/xyz", "content": "hi"}, ""},
		{"absent content is still missing", map[string]any{"path": "/tmp/xyz"}, "content"},
		{"nil content is still missing", map[string]any{"path": "/tmp/xyz", "content": nil}, "content"},
		{"path has no minLength, so empty is still missing", map[string]any{"path": "", "content": "hi"}, "path"},
	}
	for _, c := range cases {
		if got := strings.Join(names(missingRequiredParams(schema, c.params)), ","); got != c.wantErr {
			t.Errorf("%s: missing=%q want %q", c.name, got, c.wantErr)
		}
	}
}

// A schema that says nothing about minLength keeps the old strictness, so the
// widening is opt-in per parameter rather than a change to every tool.
func TestEmptyStringStillMissingWithoutOptIn(t *testing.T) {
	schema, err := parseToolSchema(json.RawMessage(`{
		"type":"object",
		"properties":{"query":{"type":"string"}},
		"required":["query"]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := names(missingRequiredParams(schema, map[string]any{"query": ""})); len(got) != 1 || got[0] != "query" {
		t.Errorf(`empty query should still be missing, got %v`, got)
	}
	if got := names(missingRequiredParams(schema, map[string]any{"query": "  "})); len(got) != 1 {
		t.Errorf(`whitespace query should still be missing, got %v`, got)
	}
}
