package plugins

import (
	"context"
	"encoding/json"
	"testing"

	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
)

type fakeTool struct{ n string }

func (t fakeTool) Name() string                { return t.n }
func (t fakeTool) Description() string         { return "" }
func (t fakeTool) Parameters() json.RawMessage { return json.RawMessage("{}") }
func (t fakeTool) Impact(map[string]any) int   { return agenttools.ImpactObserve }
func (t fakeTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

type fakePlugin struct{ name string }

func (f fakePlugin) Name() string        { return f.name }
func (f fakePlugin) Description() string { return "fake " + f.name }
func (f fakePlugin) Register(h Host) {
	h.AddTool(fakeTool{f.name + "_tool"})
	h.RegisterBinaryDecoder("application/x-"+f.name, func([]byte) (string, error) { return "", nil })
}

// The plugin lifecycle: Add records it; Catalog shows it inactive; Activate runs
// Register (returning its tools) and marks it active; MarkActive covers the
// runtime path (plugin_enable).
func TestRegistryLifecycle(t *testing.T) {
	Add(fakePlugin{"testfake"})

	if _, ok := Get("testfake"); !ok {
		t.Fatal("Get(testfake) miss after Add")
	}
	if IsActive("testfake") {
		t.Fatal("plugin should not be active before Activate")
	}

	found := false
	for _, i := range Catalog() {
		if i.Name == "testfake" {
			found = true
			if i.Active {
				t.Fatal("catalog entry should be inactive before Activate")
			}
			if i.Description != "fake testfake" {
				t.Fatalf("catalog description = %q", i.Description)
			}
		}
	}
	if !found {
		t.Fatal("testfake missing from Catalog")
	}

	tools, on, missing := Activate([]string{"testfake", "nope"}, Deps{})
	if len(on) != 1 || on[0] != "testfake" {
		t.Fatalf("on = %v, want [testfake]", on)
	}
	if len(missing) != 1 || missing[0] != "nope" {
		t.Fatalf("missing = %v, want [nope]", missing)
	}
	if len(tools) != 1 || tools[0].Name() != "testfake_tool" {
		t.Fatalf("tools = %v, want one testfake_tool", tools)
	}
	if !IsActive("testfake") {
		t.Fatal("plugin should be active after Activate")
	}

	// MarkActive is the runtime (plugin_enable) path.
	Add(fakePlugin{"rt"})
	if IsActive("rt") {
		t.Fatal("rt should be inactive until MarkActive")
	}
	MarkActive("rt")
	if !IsActive("rt") {
		t.Fatal("MarkActive did not take")
	}
}
