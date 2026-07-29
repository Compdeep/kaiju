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
	msg, ok := tools.ParseToolMessage(out)
	if !ok || msg.Kind != "debug" {
		t.Fatalf("debug output is not a debug envelope: %s", out)
	}
	var env struct {
		Problem string `json:"problem"`
	}
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		t.Fatalf("debug data not valid JSON: %v (%s)", err, msg.Data)
	}
	if env.Problem != "boom at x.go:3" {
		t.Fatalf("debug problem = %q, want the brief echoed back", env.Problem)
	}
}

// The Holmes-graft trigger: debugProblem must detect a debug node and return its
// brief — from the typed body and the legacy string. If this breaks, the debug
// super-tool stops spawning investigations.
func TestDebugProblem_TriggersGraft(t *testing.T) {
	typed := toolMessageBody{msg: tools.ToolOK("debug", "", map[string]string{"problem": "nil deref"})}
	if p, ok := debugProblem(nodeCompletion{Body: typed}); !ok || p != "nil deref" {
		t.Fatalf("typed debug body → (%q,%v), want (nil deref,true)", p, ok)
	}
	if p, ok := debugProblem(nodeCompletion{Result: `{"type":"debug","problem":"legacy"}`}); !ok || p != "legacy" {
		t.Fatalf("legacy debug string → (%q,%v), want (legacy,true)", p, ok)
	}
	if _, ok := debugProblem(nodeCompletion{Result: "just text"}); ok {
		t.Fatal("non-debug output must not trigger the graft")
	}
}
