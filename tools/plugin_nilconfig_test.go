package tools

import (
	"context"
	"strings"
	"testing"

	agenttools "github.com/Compdeep/kaiju/agent/tools"
)

// Constructing without a configuration used to dereference it and bring the
// process down at startup, before any tool had run and with nothing naming the
// tool that did it.
func TestPluginTools_NilConfigDoesNotPanicOnConstruction(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("construction panicked: %v", r)
		}
	}()
	if NewPluginEnable(agenttools.NewRegistry(), nil, NewService(t.TempDir())) == nil {
		t.Fatal("want a tool")
	}
	if NewPluginOption(nil) == nil {
		t.Fatal("want a tool")
	}
}

// And calling it says so, as a failure the run can read and the coverage
// statement can report — rather than a crash or a silent success.
func TestPluginTools_NilConfigFailsWhenCalled(t *testing.T) {
	cases := map[string]agenttools.TypedExecutor{
		"plugin_enable": NewPluginEnable(agenttools.NewRegistry(), nil, NewService(t.TempDir())),
		"plugin_option": NewPluginOption(nil),
	}
	for name, tool := range cases {
		msg, err := tool.ExecuteTyped(context.Background(), map[string]any{"name": "anything"})
		if err != nil {
			t.Errorf("%s returned a transport error rather than a failed result: %v", name, err)
			continue
		}
		if msg.Status != agenttools.StatusError {
			t.Errorf("%s status = %q, want error", name, msg.Status)
		}
		if !strings.Contains(msg.Detail, "no configuration") {
			t.Errorf("%s detail should say what is missing, got %q", name, msg.Detail)
		}
	}
}

// A tool that fails is a coverage gap, which is the point of reporting it as a
// failed result rather than an error: the run continues and the answer is told.
func TestPluginTools_NilConfigIsAGapNotACrash(t *testing.T) {
	msg, err := NewPluginOption(nil).ExecuteTyped(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Type != "plugin" || msg.Status != agenttools.StatusError {
		t.Fatalf("envelope = type %q status %q", msg.Type, msg.Status)
	}
}
