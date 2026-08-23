package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// A reader of the trace was shown the rendered text cut at 512 characters. A
// tool returning one long value showed the start of that value and none of its
// other fields — so a file it had written was invisible in the run that wrote
// it. Every field is sent; only long values are cut.
func TestNodePayload_KeepsEveryFieldAndCutsOnlyLongValues(t *testing.T) {
	long := strings.Repeat("x", 5000)
	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "read", ToolName: "reader"})
	g.SetBody(id, toolMessageBody{msg: toolapi.ToolMessage{
		Type:   "page",
		Status: toolapi.StatusOK,
		Data:   json.RawMessage(`{"content":"` + long + `","kept_at":"kept/doc.txt","bytes":1219043,"nested":{"deep":"short"},"list":["a","b"]}`),
	}})

	var got map[string]any
	if err := json.Unmarshal(nodePayload(g.Get(id)), &got); err != nil {
		t.Fatalf("payload unreadable: %v", err)
	}

	for _, field := range []string{"content", "kept_at", "bytes", "nested", "list"} {
		if _, present := got[field]; !present {
			t.Fatalf("every field must survive; %q did not: %v", field, got)
		}
	}
	if got["kept_at"] != "kept/doc.txt" || got["bytes"] != float64(1219043) {
		t.Fatalf("short values must be untouched: %v", got)
	}
	cut, _ := got["content"].(string)
	if len(cut) > payloadValueChars+40 || !strings.Contains(cut, "5000 chars") {
		t.Fatalf("a long value must be cut and say how long it was, got %d chars", len(cut))
	}
	if inner, ok := got["nested"].(map[string]any); !ok || inner["deep"] != "short" {
		t.Fatalf("nesting must survive: %v", got["nested"])
	}
	if list, ok := got["list"].([]any); !ok || len(list) != 2 {
		t.Fatalf("list length must survive: %v", got["list"])
	}
}

func TestNodePayload_NilWithoutABody(t *testing.T) {
	if nodePayload(nil) != nil {
		t.Fatal("no node, no payload")
	}
	if nodePayload(&Node{}) != nil {
		t.Fatal("no body, no payload")
	}
}
