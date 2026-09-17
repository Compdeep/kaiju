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

		// An empty call has no values to be wrong about, so a schema that
		// reports anything here faults a call for a field nobody sent.
		if vs := toolapi.ValidateParams(raw, map[string]any{}); len(vs) > 0 {
			t.Errorf("%s: an empty call reports %v", name, vs)
		}
	}
}
