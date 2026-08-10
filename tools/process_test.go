package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The pid comes back in the payload, not only in the sentence. A later step
// confirming the process is gone needs the number, and reading it back out of
// rendered text is what the payload exists to avoid.
func TestProcessKill_ReportsWhatItSignalled(t *testing.T) {
	// pid 1 is refused before anything is signalled, which is the only pid safe
	// to name in a test. What is checked is the refusal, not the kill.
	_, err := NewProcessKill().ExecuteTyped(context.Background(), map[string]any{"pid": 1})
	if err == nil {
		t.Fatal("pid 1 should be refused")
	}
	if !strings.Contains(err.Error(), "refusing") {
		t.Errorf("the refusal should say so: %v", err)
	}

	// And the schema declares the fields the tool sets, which is what a planner
	// reads to write a reference into them.
	var declared struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(toolapi.PayloadSchema(NewProcessKill().OutputSchema()), &declared); err != nil {
		t.Fatalf("output schema: %v", err)
	}
	for _, field := range []string{"pid", "force", "ok", "output"} {
		if _, ok := declared.Properties[field]; !ok {
			t.Errorf("the schema does not declare %q, so a planner is never told it exists", field)
		}
	}
}
