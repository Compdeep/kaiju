package agent

import (
	"context"
	"strings"
	"testing"
)

// Behaviour that only makes sense alongside compute is derived from whether the
// run can reach compute, not from a second setting that can disagree with it.

func TestCanReachTool_UnregisteredIsUnreachable(t *testing.T) {
	a, err := New(Config{
		ModelConfig: ModelConfig{LLMEndpoint: "http://127.0.0.1:1", LLMAPIKey: "x", LLMModel: "none"},
		PathConfig:  PathConfig{DataDir: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.CanReachTool("no_such_tool", nil) {
		t.Error("a tool that was never registered reports as reachable")
	}
}

func TestCanReachTool_ScopeNarrowsARegisteredTool(t *testing.T) {
	a, err := New(Config{
		ModelConfig: ModelConfig{LLMEndpoint: "http://127.0.0.1:1", LLMAPIKey: "x", LLMModel: "none"},
		PathConfig:  PathConfig{DataDir: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := a.registry.Register(NewComputeTool(a)); err != nil {
		t.Fatalf("register compute: %v", err)
	}

	cases := []struct {
		name  string
		scope *ResolvedScope
		want  bool
	}{
		{"the local operator, unrestricted", nil, true},
		{"a scope naming everything", &ResolvedScope{AllowedTools: map[string]bool{"*": true}}, true},
		{"a scope naming compute", &ResolvedScope{AllowedTools: map[string]bool{computeToolName: true}}, true},
		{"a scope that leaves compute out", &ResolvedScope{AllowedTools: map[string]bool{"web_search": true}}, false},
		{"a scope naming nothing", &ResolvedScope{AllowedTools: map[string]bool{}}, false},
	}
	for _, c := range cases {
		if got := a.CanReachTool(computeToolName, c.scope); got != c.want {
			t.Errorf("%s: CanReachTool = %v, want %v", c.name, got, c.want)
		}
	}
}

// The flag means what it says: deep is refused, shallow is not. This is the
// chokepoint that never ran while the tool was unregistered.
func TestDisableCoding_RefusesDeepAtTheChokepoint(t *testing.T) {
	a, err := New(Config{
		ModelConfig:   ModelConfig{LLMEndpoint: "http://127.0.0.1:1", LLMAPIKey: "x", LLMModel: "none"},
		PathConfig:    PathConfig{DataDir: t.TempDir()},
		ComputeConfig: ComputeConfig{DisableCoding: true},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// runCompute stamps the node it was given before it checks anything, so the
	// context needs one even though this call never gets past the mode check.
	ec := &ExecuteContext{Ctx: context.Background(), Node: &Node{ID: "n1", Type: NodeCompute}}
	_, err = a.runCompute(ec, map[string]any{"goal": "build me a web application", "mode": "deep"})
	if err == nil {
		t.Fatal("deep compute was allowed with coding disabled")
	}
	if !strings.Contains(err.Error(), "code generation is disabled") {
		t.Errorf("refused for the wrong reason: %v", err)
	}
}

// And the tool is still there to be refused at, which is what changed.
func TestDisableCoding_LeavesTheToolRegistrable(t *testing.T) {
	a, err := New(Config{
		ModelConfig:   ModelConfig{LLMEndpoint: "http://127.0.0.1:1", LLMAPIKey: "x", LLMModel: "none"},
		PathConfig:    PathConfig{DataDir: t.TempDir()},
		ComputeConfig: ComputeConfig{DisableCoding: true},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !a.cfg.DisableCoding {
		t.Fatal("the flag did not survive construction, so this test proves nothing")
	}
	// The tool must still be constructible and registrable with the flag set —
	// which is the whole point: the refusal happens per call, on mode.
	if err := a.registry.Register(NewComputeTool(a)); err != nil {
		t.Fatalf("compute could not be registered with coding disabled: %v", err)
	}
	if !a.CanReachTool(computeToolName, nil) {
		t.Error("compute is unreachable with the flag set; shallow compute is supposed to still work")
	}
}
