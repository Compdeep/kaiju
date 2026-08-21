package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Each action of net_info returns its result in a payload field a later step can
// name. Four of the five always did; ports and connections returned only a count
// and left the listing reachable as the text a model reads. A planner wanting the
// listing had no field to reference, so it referenced the only other one
// declared for that action — "output", which carries the failed command's text
// and is absent whenever the command works.
var netInfoResultField = map[string]string{
	"interfaces":   "interfaces",
	"dns":          "addresses",
	"connectivity": "reachable",
	"ports":        "ports",
	"connections":  "connections",
}

func TestNetInfo_EveryActionDeclaresAFieldForItsResult(t *testing.T) {
	payload := toolapi.PayloadSchema((&NetInfo{}).OutputSchema())
	if payload == nil {
		t.Fatal("net_info's payload schema does not parse, so a planner is shown no fields at all")
	}
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("payload schema unreadable: %v", err)
	}
	for action, field := range netInfoResultField {
		if _, declared := parsed.Properties[field]; !declared {
			t.Fatalf("action %q returns its result in %q, which the schema does not declare — a step that needs it has nothing to reference",
				action, field)
		}
	}
	// The failure diagnostic must stay distinct from the result, or the two
	// become interchangeable to anyone reading the field list.
	if _, declared := parsed.Properties["output"]; !declared {
		t.Fatal("output is set on the connections failure path, so it must stay declared")
	}
}

// The two actions that read a command's text now keep its rows. Skipped where the
// host has nothing to list, since that is a property of the machine, not the code.
func TestNetInfo_PortsAndConnectionsCarryTheirRows(t *testing.T) {
	for _, action := range []string{"ports", "connections"} {
		msg, err := (&NetInfo{}).ExecuteTyped(context.Background(), map[string]any{"action": action})
		if err != nil {
			t.Skipf("%s is not listable on this host: %v", action, err)
		}
		if msg.Status != toolapi.StatusOK {
			t.Skipf("%s returned %s on this host", action, msg.Status)
		}
		var got map[string]any
		if err := json.Unmarshal(msg.Data, &got); err != nil {
			t.Fatalf("%s payload unreadable: %v", action, err)
		}
		rows, present := got[action].([]any)
		if !present {
			t.Fatalf("%s returned no %q field, so its listing is reachable only as text: %v", action, action, got)
		}
		if count, ok := got["count"].(float64); ok && int(count) != len(rows) {
			t.Fatalf("%s says count=%d but carries %d rows", action, int(count), len(rows))
		}
		if msg.Content == "" {
			t.Fatalf("%s must still return the readable text a model reads", action)
		}
	}
}
