package agent

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The Coder used to be offered both reply shapes on every call, with which one
// applies stated only in their descriptions. A Coder naming a brand new file
// could reply with text replacements for content nobody had written, and the
// run failed on "no such file or directory". Two runs with identical inputs
// went opposite ways — one wrote its file, one failed on it — so it came down
// to which allowed answer the model happened to give.
func TestCoderSchema_OffersEditsOnlyWhenAFileExists(t *testing.T) {
	props := func(editable bool) map[string]json.RawMessage {
		t.Helper()
		var parsed struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		raw := coderSchema(editable).Function.Parameters
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("editable=%v: schema is not valid JSON: %v", editable, err)
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

// A reply may name a file and a language and stop there — saying what is about
// to be written without ever saying what goes in it. One did:
// {"filename": "privesc_checklist.json", "language": "json"}, nothing else. The
// step then failed on a guard about a missing run command, which is a second
// symptom of the same silence. When writing the file whole is the only shape on
// offer, the content comes with it.
func TestCoderSchema_DemandsFileContentWhenThereIsNothingToEdit(t *testing.T) {
	required := func(editable bool) []string {
		t.Helper()
		var parsed struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(coderSchema(editable).Function.Parameters, &parsed); err != nil {
			t.Fatalf("editable=%v: schema is not valid JSON: %v", editable, err)
		}
		return parsed.Required
	}

	if got := required(false); !slices.Contains(got, "code") {
		t.Errorf("with no file to edit, required = %v, want it to demand code", got)
	}
	// A file that is already there takes either shape, and a required array
	// cannot say "one of these two", so neither is demanded.
	if got := required(true); slices.Contains(got, "code") || slices.Contains(got, "edits") {
		t.Errorf("with a file to edit, required = %v, want neither shape demanded", got)
	}
	for _, editable := range []bool{false, true} {
		got := required(editable)
		for _, field := range []string{"language", "filename"} {
			if !slices.Contains(got, field) {
				t.Errorf("editable=%v: required = %v, want it to demand %s", editable, got, field)
			}
		}
	}
}

// Every field the engine reads off a Coder reply has to be one the Coder was
// allowed to send.
//
// `execute` sat in both reply structs and the Coder's prompt called it
// mandatory, while no schema ever declared it. The Coder replies through the
// schema, so the field could not arrive, and the only remaining source was a
// language lookup that knows python, javascript and bash. Every shallow compute
// naming any other language failed on a guard demanding the field.
//
// Reading the structs by reflection rather than by a written list means a field
// added to either one later is checked here without anyone remembering to.
func TestCoderSchema_DeclaresEveryFieldTheEngineReads(t *testing.T) {
	for _, editable := range []bool{false, true} {
		var parsed struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(coderSchema(editable).Function.Parameters, &parsed); err != nil {
			t.Fatalf("editable=%v: schema is not valid JSON: %v", editable, err)
		}
		for _, shape := range []any{coderEditReply{}, coderWriteReply{}} {
			rt := reflect.TypeOf(shape)
			for i := range rt.NumField() {
				name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
				if name == "" || name == "-" {
					continue
				}
				// The one field deliberately withheld, and only in the case
				// where there is no file whose text could be replaced. The test
				// above holds that withholding.
				if name == "edits" && !editable {
					continue
				}
				if _, declared := parsed.Properties[name]; !declared {
					t.Errorf("editable=%v: %s reads %q, which coderSchema does not declare — the Coder has no way to send it",
						editable, rt.Name(), name)
				}
			}
		}
	}
}
