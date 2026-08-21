package agent

import (
	"encoding/json"
	"testing"
)

// The Coder used to be offered both reply shapes on every call, with which one
// applies stated only in their descriptions. A Coder naming a brand new file
// could reply with text replacements for content nobody had written, and the
// run failed on "no such file or directory". Two runs with identical inputs
// went opposite ways — one wrote its file, one failed on it — so it came down
// to which allowed answer the model happened to give.
func TestCoderToolDef_OffersEditsOnlyWhenAFileExists(t *testing.T) {
	props := func(editable bool) map[string]json.RawMessage {
		t.Helper()
		var parsed struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		raw := coderToolDef(editable).Function.Parameters
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("editable=%v: schema is not valid JSON: %v", editable, err)
		}
		if len(parsed.Required) != 2 || parsed.Required[0] != "language" || parsed.Required[1] != "filename" {
			t.Fatalf("editable=%v: required changed to %v", editable, parsed.Required)
		}
		return parsed.Properties
	}

	// Nothing to edit: replacing text in a file that does not exist cannot be
	// carried out, so it is not on offer.
	withoutFile := props(false)
	if _, offered := withoutFile["edits"]; offered {
		t.Fatal("with no existing file, edits must not be offered")
	}
	if _, offered := withoutFile["code"]; !offered {
		t.Fatal("writing the file whole must always be available")
	}

	// A file is there: both shapes are sensible, so both stay.
	withFile := props(true)
	if _, offered := withFile["edits"]; !offered {
		t.Fatal("with an existing file, edits must be offered")
	}
	if _, offered := withFile["code"]; !offered {
		t.Fatal("replacing an existing file wholesale must stay possible")
	}
}

// The eval harness exercises both shapes, so it must keep seeing both.
func TestEditorEvalBundle_StillSeesBothShapes(t *testing.T) {
	_, def := EditorEvalBundle()
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(def.Function.Parameters, &parsed); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	for _, field := range []string{"code", "edits", "language", "filename"} {
		if _, offered := parsed.Properties[field]; !offered {
			t.Fatalf("the harness must still see %q", field)
		}
	}
}
