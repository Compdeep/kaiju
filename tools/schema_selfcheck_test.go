package tools

import (
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

/*
 * Every core tool's schema is one the validator can read and hold a call to.
 *
 * The dispatcher now checks each call against the schema its tool declared, so a
 * schema that is malformed, or that declares a type nothing can satisfy, stops
 * being a document nobody reads and becomes a rule every call is measured by. A
 * tool whose schema is wrong about itself would report a violation on every
 * correct call, and the place to find that is here rather than in a log.
 */
func TestEverySchemaIsReadableAndSelfConsistent(t *testing.T) {
	reg := toolapi.NewRegistry()
	names, err := Register(reg, Deps{Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no tools registered — this test would pass by vacancy")
	}

	for _, name := range names {
		tool, ok := reg.Get(name)
		if !ok {
			t.Errorf("%s: registered and then not found", name)
			continue
		}
		raw := tool.Parameters()
		if len(raw) == 0 {
			continue // a tool that declared nothing has promised nothing
		}

		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Errorf("%s: parameters are not JSON, so nothing can be held to them: %v", name, err)
			continue
		}

		// An empty call must report exactly the tool's required fields and
		// nothing else. Anything more means the schema faults a call for a
		// field it did not ask for.
		vs := toolapi.ValidateParams(raw, map[string]any{})
		required := map[string]bool{}
		for _, r := range requiredOf(doc) {
			required[r] = true
		}
		for _, v := range vs {
			if v.Declared != "required" {
				t.Errorf("%s: an empty call reports %v — the schema faults a field it did not ask for", name, v)
				continue
			}
			if !required[v.Path] {
				t.Errorf("%s: %q reported absent but is not in required", name, v.Path)
			}
		}
		if len(vs) != len(required) {
			t.Errorf("%s: %d required fields, %d reported absent", name, len(required), len(vs))
		}
	}
}

func requiredOf(doc map[string]any) []string {
	list, _ := doc["required"].([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
