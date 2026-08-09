package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Some tools exist to be asked for — raising a ticket, opening an incident,
// escalating to a person. Each records a human's judgement, and a run with
// nobody watching has none to record. Offering them to an unattended run
// invites a model to manufacture the judgement itself.

type plainTool struct{ name string }

func (p *plainTool) Name() string              { return p.name }
func (*plainTool) Description() string         { return "a tool" }
func (*plainTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (*plainTool) Impact(map[string]any) int   { return 0 }
func (*plainTool) RequiresTarget() bool        { return false }
func (*plainTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

type humanTool struct{ plainTool }

func (*humanTool) RequiresHuman() bool { return true }

func agentWithTools(t *testing.T) *Agent {
	t.Helper()
	reg := toolapi.NewRegistry()
	if err := reg.Register(&plainTool{name: "get_alerts"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Register(&humanTool{plainTool{name: "raise_ticket"}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	return &Agent{registry: reg}
}

func has(list []string, name string) bool {
	for _, n := range list {
		if n == name {
			return true
		}
	}
	return false
}

func TestInteractiveToolsAreOfferedWhenSomeoneIsThere(t *testing.T) {
	got := agentWithTools(t).relevantTools(context.Background(), Trigger{Type: "chat_query"})
	if !has(got, "raise_ticket") {
		t.Errorf("a human-only tool was withheld from an attended run: %v", got)
	}
	if !has(got, "get_alerts") {
		t.Errorf("an ordinary tool went missing: %v", got)
	}
}

func TestInteractiveToolsAreWithheldFromUnattendedRuns(t *testing.T) {
	got := agentWithTools(t).relevantTools(context.Background(),
		Trigger{Type: "event", ExecutionMode: "autonomous"})
	if has(got, "raise_ticket") {
		t.Errorf("a human-only tool was offered to an unattended run: %v", got)
	}
	if !has(got, "get_alerts") {
		t.Errorf("an ordinary tool was withheld from an unattended run: %v", got)
	}
}

// An undeclared tool is usable anywhere. The opposite default to
// RequiresTarget, and deliberately so: most work happens unattended, and
// defaulting the other way would empty the tool list of every automated run.
func TestUndeclaredToolsAreUsableUnattended(t *testing.T) {
	if toolapi.RequiresHuman(&plainTool{name: "x"}) {
		t.Error("an undeclared tool was treated as human-only")
	}
}
