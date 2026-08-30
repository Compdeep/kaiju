package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A parameter required only in some modes is marked required by neither the
// signature nor the schema's flat "required" list, because it is not required in
// every mode. validatePlanParams enforces those rules anyway, from the same
// schema — so a planner shown only the flat list writes exactly what it was told
// is valid, is rejected for it, and re-plans against the same signature to
// produce the same step.
//
// Measured on a live node: net_info(action*, host, port) was shown, the planner
// wrote {"action":"dns"}, and the run was abandoned after three corrections for
// one missing parameter.
//
// This asserts the two agree: what the dispatcher rejects for, the index states.
func TestToolIndexStatesTheRulesTheDispatcherEnforces(t *testing.T) {
	// The shape net_info uses: host required only for two of five actions.
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["interfaces", "connectivity", "dns", "ports"]},
			"host":   {"type": "string"},
			"port":   {"type": "integer"}
		},
		"required": ["action"],
		"allOf": [
			{"if": {"properties": {"action": {"enum": ["connectivity", "dns"]}}, "required": ["action"]},
			 "then": {"required": ["host"]}}
		],
		"additionalProperties": false
	}`)

	lines := conditionalRequirementLines(schema)
	if len(lines) == 0 {
		t.Fatal("the index says nothing about a rule the dispatcher rejects steps for")
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{"host", "action", "connectivity", "dns"} {
		if !strings.Contains(got, want) {
			t.Errorf("the stated rule does not name %q: %s", want, got)
		}
	}

	// The words have to be the dispatcher's, or a planner reading one and being
	// rejected by the other has no way to connect them.
	parsed, err := parseToolSchema(schema)
	if err != nil {
		t.Fatalf("parseToolSchema: %v", err)
	}
	if len(parsed.Conditional) != 1 {
		t.Fatalf("want one rule, got %d", len(parsed.Conditional))
	}
	if !strings.Contains(got, parsed.Conditional[0].describe()) {
		t.Errorf("the index words the rule differently from the rejection:\n index: %s\n reject: %s",
			got, parsed.Conditional[0].describe())
	}
}

// A tool with no conditionals gains no lines: this must not pad an index that is
// already the largest thing in the planner's prompt.
func TestToolIndexAddsNothingForAPlainSchema(t *testing.T) {
	for _, raw := range []string{
		`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`,
		`{"type":"object","properties":{}}`,
		``,
		`{not json`,
	} {
		if lines := conditionalRequirementLines(json.RawMessage(raw)); len(lines) != 0 {
			t.Errorf("schema %q produced %d line(s), want none", raw, len(lines))
		}
	}
}

// A tool's index entry has to state every rule its own schema carries, because
// the entry is the only description of the tool the planner is given. This is
// the end-to-end assertion: a schema goes in, and the words the dispatcher
// would reject a step with come out.
func TestIndexEntryStatesTheRuleForARegisteredTool(t *testing.T) {
	reg := toolapi.NewRegistry()
	if err := reg.Register(&conditionalStub{}); err != nil {
		t.Fatalf("register: %v", err)
	}

	entry := toolIndexEntry(reg, "stub_net")
	if entry == "" {
		t.Fatal("no index entry for a registered tool")
	}
	// The flat signature marks only action; that is the whole problem.
	if !strings.Contains(entry, "action*") {
		t.Errorf("the signature no longer marks the unconditionally required parameter:\n%s", entry)
	}
	// And the rule the flat list cannot express is stated alongside it.
	if !strings.Contains(entry, "host required when") {
		t.Errorf("the entry does not state when host is required, which is what the run was abandoned for:\n%s", entry)
	}
	for _, mode := range []string{"connectivity", "dns"} {
		if !strings.Contains(entry, mode) {
			t.Errorf("the entry does not name the %q mode the rule applies to:\n%s", mode, entry)
		}
	}
}

// conditionalStub carries the schema shape this is about: one parameter
// required only in some of another parameter's modes. Named for what it tests
// rather than mirroring a real tool, so a change to net_info cannot quietly
// turn this into a test of nothing.
type conditionalStub struct{}

func (c *conditionalStub) Name() string        { return "stub_net" }
func (c *conditionalStub) Description() string { return "a tool whose requirements depend on its mode" }
func (c *conditionalStub) Impact(map[string]any) int {
	return 0
}
func (c *conditionalStub) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}
func (c *conditionalStub) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["interfaces", "connectivity", "dns"]},
			"host":   {"type": "string"}
		},
		"required": ["action"],
		"allOf": [
			{"if": {"properties": {"action": {"enum": ["connectivity", "dns"]}}, "required": ["action"]},
			 "then": {"required": ["host"]}}
		],
		"additionalProperties": false
	}`)
}
