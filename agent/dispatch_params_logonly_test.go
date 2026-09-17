package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

/*
 * The parameter check in executeToolNode reports and never refuses.
 *
 * It was added to say when a call does not match the shape its tool declared,
 * because a declared string arriving as a number becomes "" rather than an
 * error, and an empty value is frequently BROADER than the one that was meant.
 * Saying so is the whole of its job. Models produce off-spec parameters
 * routinely and tools cope with slightly wrong ones today, so a check that
 * refused on sight would fail runs that currently work.
 *
 * This holds that line. If someone later makes it reject, these fail, and the
 * decision to start refusing calls becomes a deliberate act with a test to
 * change rather than a consequence of tightening a validator.
 */

// strictSchemaTool declares types, an enum and a required field, and records
// what it was actually given.
type strictSchemaTool struct{ got map[string]any }

func (s *strictSchemaTool) Name() string              { return "strict" }
func (s *strictSchemaTool) Description() string       { return "declares a precise shape" }
func (s *strictSchemaTool) Impact(map[string]any) int { return toolapi.ImpactObserve }
func (s *strictSchemaTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {"type": "string"},
			"count":   {"type": "integer", "minimum": 1, "maximum": 10},
			"mode":    {"type": "string", "enum": ["run", "dry"]}
		},
		"required": ["command"]
	}`)
}
func (s *strictSchemaTool) Execute(ctx context.Context, p map[string]any) (string, error) {
	return toolapi.StringResult(s.ExecuteTyped(ctx, p))
}
func (s *strictSchemaTool) ExecuteTyped(_ context.Context, p map[string]any) (toolapi.ToolMessage, error) {
	s.got = p
	return toolapi.ToolOK("strict", "ran", map[string]any{"ok": true}), nil
}

func runWithParams(t *testing.T, params map[string]any) (*strictSchemaTool, string, error) {
	t.Helper()
	reg, gate, _ := newTestStack(t)
	tool := &strictSchemaTool{}
	registry := toolapi.NewRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatalf("register: %v", err)
	}
	a := &Agent{registry: registry, gate: gate, intentRegistry: reg}
	graph := NewGraph()
	id := graph.AddNode(&Node{Type: NodeTool, ToolName: "strict"})
	result, _, err := a.executeToolNode(context.Background(), graph.Get(id), graph,
		NewBudget(20, 5, 20, 5, time.Minute), "strict", params, "", gates.Intent(0), nil)
	return tool, result, err
}

/*
 * Every shape the checker has an opinion about still reaches the tool, with the
 * parameters unaltered. A checker that dropped or corrected a value would be
 * worse than one that refused: the tool would run on something nobody sent.
 */
func TestAViolatingCallStillRunsAndIsUnaltered(t *testing.T) {
	for _, c := range []struct {
		name   string
		params map[string]any
	}{
		{"wrong type", map[string]any{"command": float64(42)}},
		{"missing required", map[string]any{"count": float64(3)}},
		{"below minimum", map[string]any{"command": "ls", "count": float64(0)}},
		{"above maximum", map[string]any{"command": "ls", "count": float64(99)}},
		{"not in enum", map[string]any{"command": "ls", "mode": "delete"}},
		{"fractional integer", map[string]any{"command": "ls", "count": 2.5}},
		{"array where a string was declared", map[string]any{"command": []any{"ls"}}},
		{"every fault at once", map[string]any{"command": true, "count": "many", "mode": 7}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tool, result, err := runWithParams(t, c.params)
			if err != nil {
				t.Fatalf("the call was refused: %v", err)
			}
			if result == "" {
				t.Error("no result — the call did not reach the tool")
			}
			if len(tool.got) != len(c.params) {
				t.Errorf("the tool saw %d parameters, was sent %d — something altered the map",
					len(tool.got), len(c.params))
			}
			for k, want := range c.params {
				if got, ok := tool.got[k]; !ok {
					t.Errorf("%q never reached the tool", k)
				} else if !sameValue(got, want) {
					t.Errorf("%q reached the tool as %#v, was sent %#v", k, got, want)
				}
			}
		})
	}
}

// A nil map is normalised to an empty one before anything reads it, and still
// reaches the tool rather than being refused for its missing required field.
func TestANilParamMapStillRuns(t *testing.T) {
	tool, _, err := runWithParams(t, nil)
	if err != nil {
		t.Fatalf("a nil map was refused: %v", err)
	}
	if tool.got == nil {
		t.Error("the tool was handed a nil map")
	}
}

// And a conforming call is unaffected, so the check is not costing correct
// calls anything either.
func TestAConformingCallIsUntouched(t *testing.T) {
	params := map[string]any{"command": "ls -la", "count": float64(3), "mode": "run"}
	tool, result, err := runWithParams(t, params)
	if err != nil || result == "" {
		t.Fatalf("a conforming call failed: %v", err)
	}
	if len(tool.got) != 3 || tool.got["command"] != "ls -la" {
		t.Errorf("the tool saw %#v", tool.got)
	}
}

func sameValue(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
