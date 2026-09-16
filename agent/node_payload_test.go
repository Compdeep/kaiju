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

/*
 * The end of a cut value survives, because the end is where a failure says what
 * it was.
 *
 * The bash tool keeps the head and the tail of stderr on purpose — a head-only
 * cut left "Traceback (most recent call last):" with no error type under it —
 * and this payload shortener then re-cut the same string head-only and threw
 * that tail away. In the run that prompted this, a step's stderr was a download
 * progress bar followed by a TypeError, and the trace carried 400 characters of
 * progress bar and no error at all.
 */
func TestNodePayload_KeepsTheEndOfALongValue(t *testing.T) {
	stderr := strings.Repeat("[####] 50% de440.bsp\r", 200) +
		"Traceback (most recent call last):\n  File \"<string>\", line 31\n" +
		"TypeError: unsupported operand type(s) for -: 'Distance' and 'Distance'\n"

	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "compute_ssb", ToolName: "bash"})
	body, err := json.Marshal(map[string]any{"stderr": stderr, "exit_code": 1})
	if err != nil {
		t.Fatal(err)
	}
	g.SetBody(id, toolMessageBody{msg: toolapi.ToolMessage{
		Type: "command", Status: toolapi.StatusError, Data: body,
	}})

	var got map[string]any
	if err := json.Unmarshal(nodePayload(g.Get(id)), &got); err != nil {
		t.Fatalf("payload unreadable: %v", err)
	}
	cut, _ := got["stderr"].(string)
	if !strings.Contains(cut, "TypeError: unsupported operand") {
		t.Errorf("the error was cut off; a reader sees a failed node with no cause:\n%s", cut)
	}
	if !strings.Contains(cut, "[####] 50%") {
		t.Errorf("the start was dropped; how the command began is also evidence:\n%s", cut)
	}
	if len(cut) > payloadValueChars+40 {
		t.Errorf("the budget grew: %d chars", len(cut))
	}
	if !strings.Contains(cut, "chars") {
		t.Errorf("a cut value must still say how long the whole thing was: %s", cut)
	}
}

// A cut lands between characters, not inside one. Cutting a multi-byte
// character in half leaves a replacement glyph that a reader cannot tell from
// the data, and json.Marshal carries it without complaint.
func TestNodePayload_DoesNotCutAMultiByteCharacterInHalf(t *testing.T) {
	long := strings.Repeat("設定ファイルが読み込めません。", 200)

	g := NewGraph()
	id := g.AddNode(&Node{Type: NodeTool, Tag: "read", ToolName: "reader"})
	body, err := json.Marshal(map[string]any{"message": long})
	if err != nil {
		t.Fatal(err)
	}
	g.SetBody(id, toolMessageBody{msg: toolapi.ToolMessage{
		Type: "page", Status: toolapi.StatusOK, Data: body,
	}})

	var got map[string]any
	if err := json.Unmarshal(nodePayload(g.Get(id)), &got); err != nil {
		t.Fatalf("payload unreadable: %v", err)
	}
	cut, _ := got["message"].(string)
	if strings.ContainsRune(cut, '�') {
		t.Errorf("a character was cut in half: %q", cut)
	}
}
