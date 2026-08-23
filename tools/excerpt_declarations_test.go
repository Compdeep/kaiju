package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// THE GUARD OVER WHAT EACH TOOL DECLARES.
//
// A tool that cuts a value down to what fits a prompt, while writing the whole
// of it to a file, declares that pairing through toolapi.Excerpting. The engine
// reads the declaration and keeps no field names of its own, so this declaration
// is the only thing standing between a step and an answer drawn from part of a
// document and reported as covering all of it.
//
// Frozen here because it can be broken silently three ways: dropping the
// interface, renaming a field on one side only, or renaming it on both and
// leaving the output schema behind. None of those fail a build.
var frozenExcerptDeclarations = map[string][]toolapi.Excerpt{
	"web_fetch": {{Field: "content", Whole: "full_content_path", Size: "bytes"}},
	"bash":      {{Field: "stdout", Whole: "output_path", Size: "output_bytes"}},
	"file_read": {{Field: "content", Whole: "path", Flag: "truncated"}},
}

func excerptDeclaringTools(t *testing.T) map[string]toolapi.Tool {
	t.Helper()
	return map[string]toolapi.Tool{
		"web_fetch": NewWebFetchIn(t.TempDir(), nil, FetchLimits{MaxBodyBytes: DefaultMaxBodyBytes}),
		"bash":      NewBash("/bin/sh", t.TempDir()),
		"file_read": NewFileRead(t.TempDir()),
	}
}

func TestFrozen_WhichToolsDeclareACutField(t *testing.T) {
	for name, tool := range excerptDeclaringTools(t) {
		declared := toolapi.GetExcerpts(tool)
		want := frozenExcerptDeclarations[name]

		if len(declared) != len(want) {
			t.Fatalf("%s declares %d entries, frozen at %d — %s",
				name, len(declared), len(want),
				"if this tool no longer cuts its output, remove it here deliberately")
		}
		for i, w := range want {
			got := declared[i]
			if got.Field != w.Field || got.Whole != w.Whole || got.Size != w.Size || got.Flag != w.Flag {
				t.Fatalf("%s declares {%s,%s,size=%s,flag=%s}, frozen at {%s,%s,size=%s,flag=%s} — a rename on one side only leaves a step reading part of something and calling it the whole",
					name, got.Field, got.Whole, got.Size, got.Flag, w.Field, w.Whole, w.Size, w.Flag)
			}
			if got.Size == "" && got.Flag == "" {
				t.Fatalf("%s declares neither a size nor a flag for %q, so nothing can tell whether a call was cut", name, got.Field)
			}
			if strings.TrimSpace(got.Use) == "" {
				t.Fatalf("%s declares no wording for %q — the refusal would tell a planner nothing about what to do instead", name, got.Field)
			}
		}
	}
}

// A declaration naming a field the tool does not return refuses nothing, because
// the payload lookup finds no whole copy and stays silent. Tying the two together
// is what makes a rename fail here rather than at run time on a large document.
func TestFrozen_EveryDeclaredFieldExistsInTheOutputSchema(t *testing.T) {
	for name, tool := range excerptDeclaringTools(t) {
		out, ok := tool.(toolapi.Outputter)
		if !ok {
			t.Fatalf("%s declares cut fields but no output schema, so nothing can reference them", name)
		}
		props := schemaProperties(t, out.OutputSchema())

		for _, d := range toolapi.GetExcerpts(tool) {
			for label, field := range map[string]string{"Field": d.Field, "Whole": d.Whole, "Size": d.Size, "Flag": d.Flag} {
				if field == "" {
					continue // a declaration uses one of Size or Flag, not both
				}
				if _, present := props[field]; !present {
					t.Fatalf("%s declares %s=%q but its output schema does not carry that field — declared: %v",
						name, label, field, sortedProps(props))
				}
			}
		}
	}
}

func schemaProperties(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var envelope struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("output schema unreadable: %v", err)
	}
	// A ${step.N.field} reference resolves against the payload under data, and
	// falls back to the envelope's own fields — file_read's text is the
	// envelope's content, not a payload field — so both levels count here.
	merged := map[string]json.RawMessage{}
	for k, v := range envelope.Properties {
		merged[k] = v
	}
	if data, wrapped := envelope.Properties["data"]; wrapped {
		var inner struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if json.Unmarshal(data, &inner) == nil {
			for k, v := range inner.Properties {
				merged[k] = v
			}
		}
	}
	return merged
}

func sortedProps(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
