package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Payload answers a different question from Evidence, and the difference is the
// whole reason it exists.
//
// Evidence is what a stage READS — the content of a page, the output of a
// command. Payload is what a stage is GIVEN — the path the page was written to,
// the exit status, the fields a later step can be wired to. A body that has both
// must not answer one with the other.
func TestPayload_IsTheToolsFieldsNotItsText(t *testing.T) {
	b := toolMessageBody{msg: toolapi.ToolMessage{
		Type:    "page",
		Status:  toolapi.StatusOK,
		Content: "the readable text of the page",
		Data:    json.RawMessage(`{"full_content_path":"fetched/solscan_123","bytes":93059}`),
	}}

	var got map[string]any
	if err := json.Unmarshal(b.Payload(), &got); err != nil {
		t.Fatalf("payload unreadable: %v", err)
	}
	if got["full_content_path"] != "fetched/solscan_123" {
		t.Errorf("the field a later step needs is not in the payload: %v", got)
	}
	if strings.Contains(string(b.Payload()), "the readable text of the page") {
		t.Error("the payload carries the content; that is Evidence's job")
	}
	if !strings.Contains(b.Evidence(), "the readable text of the page") {
		t.Error("Evidence lost the content")
	}
}

// A long value is cut; every key, every nesting level and every list length
// survives. Which fields a stage can see must not depend on how much text one
// of them happens to hold — that is how a written file stayed invisible in the
// run that wrote it.
func TestPayload_CutsLongValuesAndKeepsTheShape(t *testing.T) {
	b := toolMessageBody{msg: toolapi.ToolMessage{
		Type:   "page",
		Status: toolapi.StatusOK,
		Data: json.RawMessage(`{"content":"` + strings.Repeat("x", 5000) +
			`","path":"kept/doc.txt","nested":{"deep":"short"},"list":["a","b"]}`),
	}}

	var got map[string]any
	if err := json.Unmarshal(b.Payload(), &got); err != nil {
		t.Fatalf("payload unreadable: %v", err)
	}
	for _, f := range []string{"content", "path", "nested", "list"} {
		if _, ok := got[f]; !ok {
			t.Fatalf("field %q was dropped: %v", f, got)
		}
	}
	if got["path"] != "kept/doc.txt" {
		t.Errorf("a short value was altered: %v", got["path"])
	}
	if cut, _ := got["content"].(string); len(cut) > payloadValueChars+40 {
		t.Errorf("a long value was not cut: %d chars", len(cut))
	}
	if inner, ok := got["nested"].(map[string]any); !ok || inner["deep"] != "short" {
		t.Errorf("nesting did not survive: %v", got["nested"])
	}
}

// A tool that declared no payload has none. Its whole result is in content, and
// content is Evidence's.
func TestPayload_NilWhenTheToolDeclaredNoFields(t *testing.T) {
	b := toolMessageBody{msg: toolapi.ToolMessage{
		Type: "note", Status: toolapi.StatusOK, Content: "just some text",
	}}
	if p := b.Payload(); p != nil {
		t.Errorf("a tool with no declared fields returned a payload: %s", p)
	}
}

// An opaque string has no fields, and says so.
//
// This is a change: nodePayload used to marshal whatever Field("") returned,
// which for a prose body is the string itself — so the trace showed a JSON
// string where a reader expected a tool's fields. Nil is the honest answer for
// a producer that never declared a shape.
func TestPayload_RawTextHasNoFields(t *testing.T) {
	if p := RawText("some prose a tool wrote").Payload(); p != nil {
		t.Errorf("an opaque string produced a payload: %s", p)
	}
	if ev := RawText("some prose a tool wrote").Evidence(); ev != "some prose a tool wrote" {
		t.Errorf("Evidence changed: %q", ev)
	}
}

// A JSON-backed body hands over its JSON; a prose-backed one hands over
// nothing. Same type, decided by what it actually holds.
func TestPayload_RawBackedOnlyWhenItIsJSON(t *testing.T) {
	if p := (RawBacked{Raw: `{"root_cause":"the port was taken"}`}).Payload(); p == nil {
		t.Error("a JSON-backed body returned no payload")
	}
	if p := (RawBacked{Raw: "a sentence, not JSON"}).Payload(); p != nil {
		t.Errorf("a prose-backed body produced a payload: %s", p)
	}
}

// Every body in the package answers all four methods. The compiler enforces
// this, but only for bodies something constructs — this is the list, so a new
// body added without Payload fails here rather than at the call that needed it.
func TestPayload_EveryBodyAnswers(t *testing.T) {
	// RawBacked is deliberately absent: it supplies three of the four and leaves
	// Summary to whatever embeds it, so it is not a NodeBody on its own.
	bodies := map[string]NodeBody{
		"RawTextBody":     RawText("x"),
		"ReflectionBody":  ReflectionBody{RawBacked: RawBacked{Raw: `{"decision":"conclude"}`}},
		"toolMessageBody": toolMessageBody{msg: toolapi.ToolMessage{Type: "t", Status: toolapi.StatusOK}},
	}
	for name, b := range bodies {
		// Nil is a valid answer; panicking or refusing to compile is not.
		_ = b.Payload()
		_ = b.Evidence()
		_ = b.Summary()
		if _, _ = b.Field(""); false {
			t.Fatal(name)
		}
	}
}
