package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// withKept must record BOTH reductions. The page is cut to what the deployment
// keeps, and the inline text is cut again to what fits a window — a result
// stating only the first lets a later stage read "the whole page is in this
// file" and take the text beside it for the whole of that file.
func TestWithKept_RecordsTheInlineTextAgainstTheFile(t *testing.T) {
	inline := strings.Repeat("a", 400)
	msg := toolapi.ToolMessage{Data: json.RawMessage(`{"content":"` + inline + `"}`)}

	out, err := withKept("https://example.test/doc", "fetched/doc.txt", 90_000, false, nil, msg, nil)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Data, &got); err != nil {
		t.Fatalf("payload unreadable: %v", err)
	}
	if got["bytes"] != float64(90_000) {
		t.Fatalf("the file size must survive, got %v", got["bytes"])
	}
	if got["content_bytes"] != float64(400) {
		t.Fatalf("content_bytes = %v, want 400", got["content_bytes"])
	}
	if got["content_truncated"] != true {
		t.Fatalf("400 bytes inline against a 90000 byte file is partial, got %v", got["content_truncated"])
	}
	if _, said := got["body_truncated"]; said {
		t.Fatal("the file was not cut, so body_truncated must stay unset")
	}
}

// A page small enough to come back whole must not be described as partial.
func TestWithKept_SaysNothingWhenTheTextIsTheWholeFile(t *testing.T) {
	inline := strings.Repeat("b", 512)
	msg := toolapi.ToolMessage{Data: json.RawMessage(`{"content":"` + inline + `"}`)}

	out, _ := withKept("https://example.test/small", "fetched/small.txt", 512, false, nil, msg, nil)
	var got map[string]any
	_ = json.Unmarshal(out.Data, &got)

	if _, said := got["content_truncated"]; said {
		t.Fatalf("content matches the file, so nothing should be claimed: %v", got)
	}
	if got["content_bytes"] != float64(512) {
		t.Fatalf("the size is still worth stating, got %v", got["content_bytes"])
	}
}
