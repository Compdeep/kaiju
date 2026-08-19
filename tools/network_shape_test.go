package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Every action of net_info answers in text and in fields.
//
// Three of the five filled only data and left content empty, and two filled only
// content and left data nil. Empty content was readable — the engine falls back to
// the raw payload — but it reached the model as JSON while the same tool's other
// actions reached it as a listing. A nil payload was worse: a later step could
// name no field of what the call returned, so it could only quote the whole
// listing back.
//
// Only the actions that need nothing off this machine are exercised. Reachability
// and name resolution are checked in their failing form, which needs no answer
// from anywhere.
func TestNetInfoEveryActionAnswersBothWays(t *testing.T) {
	n := NewNetInfo()
	runs := []struct {
		name   string
		params map[string]any
	}{
		{"ports", map[string]any{"action": "ports"}},
		{"connections", map[string]any{"action": "connections"}},
		{"interfaces", map[string]any{"action": "interfaces"}},
		// Port 1 on this host, where nothing listens: refused at once, no network.
		{"connectivity", map[string]any{"action": "connectivity", "host": "127.0.0.1", "port": 1}},
	}

	for _, r := range runs {
		t.Run(r.name, func(t *testing.T) {
			msg, err := n.ExecuteTyped(context.Background(), r.params)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if msg.Status == toolapi.StatusEmpty {
				// Nothing to report is a legitimate answer and carries no payload.
				// It says so in detail, which is what makes it different from the
				// silence this test is about.
				if msg.Detail == "" {
					t.Error("reported nothing found without saying so")
				}
				return
			}
			if strings.TrimSpace(msg.Content) == "" {
				t.Error("no text: anything reading content sees an answer with nothing in it")
			}
			if len(msg.Data) == 0 {
				t.Fatal("no payload: a later step can name no field of this result")
			}
			var payload map[string]any
			if err := json.Unmarshal(msg.Data, &payload); err != nil {
				t.Fatalf("payload is not an object: %v", err)
			}
			if payload["action"] != r.params["action"] {
				t.Errorf("payload action = %v, asked for %v", payload["action"], r.params["action"])
			}
		})
	}
}

// The count is the number of listeners, not the number that fitted in 4KB.
//
// The text is cut at 4096 bytes. Counting after the cut would have made the number
// a property of the buffer.
func TestNetInfoPortsCountsBeforeTruncating(t *testing.T) {
	msg, err := NewNetInfo().ExecuteTyped(context.Background(), map[string]any{"action": "ports"})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if msg.Status == toolapi.StatusEmpty {
		t.Skip("nothing is listening on this host")
	}
	var payload struct {
		Count     int  `json:"count"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatalf("%v", err)
	}
	rows := 0
	for i, line := range strings.Split(strings.TrimRight(msg.Content, "\n"), "\n") {
		if i > 0 && strings.TrimSpace(line) != "" {
			rows++
		}
	}
	if payload.Truncated {
		if payload.Count <= rows {
			t.Errorf("the text was cut yet count (%d) is not more than the rows left in it (%d)", payload.Count, rows)
		}
		return
	}
	if payload.Count != rows {
		t.Errorf("count = %d, rows in the listing = %d", payload.Count, rows)
	}
}

// The declared payload names the fields a planner may read.
//
// It declared none of them, on a tool whose result is five different objects.
func TestNetInfoDeclaresItsPayloadFields(t *testing.T) {
	schema := string(NewNetInfo().OutputSchema())
	for _, field := range []string{"action", "count", "interfaces", "reachable", "latency_ms", "addresses", "truncated"} {
		if !strings.Contains(schema, `"`+field+`"`) {
			t.Errorf("the declared output does not name %s, which a result carries", field)
		}
	}
}
