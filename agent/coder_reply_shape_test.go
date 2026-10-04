package agent

import (
	"encoding/json"
	"reflect"
	"slices"
	"sort"
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

// The reply must say WHICH of the four things happened, before describing it.
//
// A reply could once name a file and a language and stop there — saying what was
// about to be written without ever saying what goes in it. One did:
// {"filename": "privesc_checklist.json", "language": "json"}, nothing else.
// Demanding `code` fixed that case and created a worse one: a coder with nothing
// useful to contribute still had to contribute a file, so one handed back 2,953
// bytes of invented TypeScript over a 2,458-byte Express server.
//
// status and summary are what is demanded now. The payload is checked against the
// status by CoderResult.Validate, which a JSON Schema `required` array cannot do —
// it has no way to say "code, but only when status is created".
func TestCoderSchema_DemandsAStatusAndASummary(t *testing.T) {
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

	for _, editable := range []bool{false, true} {
		got := required(editable)
		for _, field := range []string{"status", "summary"} {
			if !slices.Contains(got, field) {
				t.Errorf("editable=%v: required = %v, want it to demand %s", editable, got, field)
			}
		}
		// Not code, and not edits. Which one applies depends on the status, and the
		// required array cannot express that — Validate does.
		for _, field := range []string{"code", "edits"} {
			if slices.Contains(got, field) {
				t.Errorf("editable=%v: required = %v, want %s left to Validate", editable, got, field)
			}
		}
	}
}

// Refusing is always on offer, whether or not the file exists.
//
// This is the field whose absence caused the damage: with no way to say "I cannot",
// the only expressible action was to write a file.
func TestCoderSchema_AlwaysOffersBlocked(t *testing.T) {
	for _, editable := range []bool{false, true} {
		var parsed struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(coderSchema(editable).Function.Parameters, &parsed); err != nil {
			t.Fatalf("editable=%v: %v", editable, err)
		}
		if _, ok := parsed.Properties["blocked"]; !ok {
			t.Errorf("editable=%v: the coder is not offered a way to refuse", editable)
		}
		got := parsed.Properties["status"].Enum
		for _, want := range []string{"created", "no_change", "blocked"} {
			if !slices.Contains(got, want) {
				t.Errorf("editable=%v: status enum = %v, want %s among them", editable, got, want)
			}
		}
		// edited is only meaningful for a file that exists.
		if editable != slices.Contains(got, "edited") {
			t.Errorf("editable=%v: status enum = %v", editable, got)
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

// Every field on EditOp is declared in the schema too.
//
// The guard above walks the top-level reply structs, so it sees `edits` and stops
// there. EditOp's own fields are what the coder actually fills in, and a field the
// engine reads but never declared is one the coder has no way to know about —
// which is how `lines` could be added to the struct and never reach a model.
func TestCoderSchema_DeclaresEveryEditField(t *testing.T) {
	var schema struct {
		Properties struct {
			Edits struct {
				Items struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"items"`
			} `json:"edits"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(coderSchema(true).Function.Parameters, &schema); err != nil {
		t.Fatalf("the coder schema will not parse: %v", err)
	}
	declared := schema.Properties.Edits.Items.Properties
	if len(declared) == 0 {
		t.Fatal("the edits item declares no properties")
	}

	t.Logf("declared on an edit: %v", keysOf(declared))
	for _, f := range jsonTagsOf(reflect.TypeOf(EditOp{})) {
		if _, ok := declared[f]; !ok {
			t.Errorf("EditOp field %q is read by the engine and not declared in the schema, "+
				"so the coder cannot know to send it", f)
		}
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func jsonTagsOf(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		out = append(out, strings.Split(tag, ",")[0])
	}
	return out
}
