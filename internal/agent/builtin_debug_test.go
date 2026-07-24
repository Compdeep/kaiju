package agent

import (
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/internal/agent/tools"
)

// TestDebugTool_Envelope verifies the debug super-tool echoes its `problem`
// into a {type:"debug"} envelope — the marker the scheduler detects to graft
// the Holmes investigation. The tool needs no live agent for this path.
func TestDebugTool_Envelope(t *testing.T) {
	d := NewDebugTool(nil)

	if d.Name() != debugToolName {
		t.Fatalf("Name() = %q, want %q", d.Name(), debugToolName)
	}
	if d.Impact(nil) != tools.ImpactAffect {
		t.Fatalf("Impact = %d, want ImpactAffect (%d) — debug fixes edit files", d.Impact(nil), tools.ImpactAffect)
	}

	out, err := d.ExecuteWithContext(nil, map[string]any{"problem": "boom at x.go:3"})
	if err != nil {
		t.Fatalf("ExecuteWithContext errored: %v", err)
	}
	var env struct {
		Type    string `json:"type"`
		Problem string `json:"problem"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("envelope not valid JSON: %v (%s)", err, out)
	}
	if env.Type != "debug" {
		t.Fatalf("envelope type = %q, want debug", env.Type)
	}
	if env.Problem != "boom at x.go:3" {
		t.Fatalf("envelope problem = %q, want the brief echoed back", env.Problem)
	}
}
