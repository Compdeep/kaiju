package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// A strict decoder writes the fields in the order the schema lists them, so the
// sent schema has to list them in the order the stage wrote them. Sorted, the
// reflector's required summary came last, the model tried to close the object
// before writing it, and it filled the reply with spaces up to the token cap.
func TestClosedSchemaKeepsTheAuthorsFieldOrder(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "object",
		"properties": {
			"decision": {"type": "string"},
			"progress": {"type": "string"},
			"summary":  {"type": "string"},
			"next":     {"type": "string"},
			"aggregate":{"type": "object", "properties": {"zeta": {"type": "string"}, "alpha": {"type": "string"}}}
		},
		"required": ["decision", "summary"]
	}`)
	out := closedSchema(raw)
	if out == nil {
		t.Fatal("closedSchema returned nil")
	}
	s := string(out)

	inOrder := func(what string, keys ...string) {
		t.Helper()
		last := -1
		for _, k := range keys {
			i := strings.Index(s, `"`+k+`":`)
			if i < 0 {
				t.Fatalf("%s: %q missing from %s", what, k, s)
			}
			if i < last {
				t.Fatalf("%s: %q is out of the author's order in %s", what, k, s)
			}
			last = i
		}
	}
	inOrder("properties", "decision", "progress", "summary", "next", "aggregate")
	inOrder("nested properties", "zeta", "alpha")

	var back struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	want := []string{"decision", "progress", "summary", "next", "aggregate"}
	if strings.Join(back.Required, ",") != strings.Join(want, ",") {
		t.Fatalf("required = %v, want %v", back.Required, want)
	}
}
