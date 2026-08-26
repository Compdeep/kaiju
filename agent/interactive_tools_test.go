package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Some tools exist to be asked for — raising a ticket, opening a case,
// escalating to a person. Each records a human's judgement, and a run with
// nobody watching has none to record. Offering them to an unattended run
// invites a model to manufacture the judgement itself.

type plainTool struct{ name string }

// wordyTool costs a stated number of characters in the planner's index, so a
// budget can be exhausted by a handful of tools rather than by hundreds.
type wordyTool struct{ name, desc string }

func (w *wordyTool) Name() string              { return w.name }
func (w *wordyTool) Description() string       { return w.desc }
func (*wordyTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (*wordyTool) Impact(map[string]any) int   { return 0 }
func (*wordyTool) RequiresTarget() bool        { return false }
func (*wordyTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

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
	if err := reg.Register(&plainTool{name: "list_records"}); err != nil {
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
	got := agentWithTools(t).relevantTools(context.Background(), nil, Trigger{Type: "chat_query"}, "list some records")
	if !has(got, "raise_ticket") {
		t.Errorf("a human-only tool was withheld from an attended run: %v", got)
	}
	if !has(got, "list_records") {
		t.Errorf("an ordinary tool went missing: %v", got)
	}
}

func TestInteractiveToolsAreWithheldFromUnattendedRuns(t *testing.T) {
	got := agentWithTools(t).relevantTools(context.Background(), nil,
		Trigger{Type: "event", ExecutionMode: "autonomous"}, "list some records")
	if has(got, "raise_ticket") {
		t.Errorf("a human-only tool was offered to an unattended run: %v", got)
	}
	if !has(got, "list_records") {
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
