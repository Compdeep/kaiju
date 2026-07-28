package tools

import (
	"encoding/json"
	"testing"
)

func TestToolMessage_Constructors(t *testing.T) {
	ok := ToolOK("page", "hello", map[string]any{"x": 1})
	if ok.Kind != "page" || ok.Status != StatusOK || ok.Content != "hello" {
		t.Fatalf("ToolOK fields wrong: %+v", ok)
	}
	if len(ok.Data) == 0 {
		t.Fatalf("ToolOK should carry marshalled data")
	}
	empty := ToolEmpty("search", "no results")
	if empty.Status != StatusEmpty || empty.Detail != "no results" || empty.Content != "" || len(empty.Data) != 0 {
		t.Fatalf("ToolEmpty wrong: %+v", empty)
	}
	fail := ToolFail("command", "exit 1", map[string]any{"code": 1})
	if fail.Status != StatusError || fail.Detail != "exit 1" || len(fail.Data) == 0 {
		t.Fatalf("ToolFail wrong: %+v", fail)
	}
	txt := ToolText("just prose")
	if txt.Kind != "text" || txt.Status != StatusOK || txt.Content != "just prose" || txt.Data != nil {
		t.Fatalf("ToolText wrong: %+v", txt)
	}
}

func TestToolMessage_JSONRoundTrip(t *testing.T) {
	orig := ToolOK("listing", "", map[string]any{"entries": []string{"a", "b"}})
	got, ok := ParseToolMessage(orig.JSON())
	if !ok {
		t.Fatalf("round-trip: ParseToolMessage failed on %s", orig.JSON())
	}
	if got.Kind != orig.Kind || got.Status != orig.Status {
		t.Fatalf("round-trip mismatch: %+v vs %+v", got, orig)
	}
	var d struct {
		Entries []string `json:"entries"`
	}
	if err := json.Unmarshal(got.Data, &d); err != nil || len(d.Entries) != 2 {
		t.Fatalf("data not preserved through round-trip: %v %s", err, got.Data)
	}
}

// The discriminator (kind + valid status) must reject legacy/raw tool output so
// only true envelopes are wrapped as typed bodies.
func TestParseToolMessage_RejectsNonEnvelopes(t *testing.T) {
	reject := map[string]string{
		"bare string":            "just a bare string",
		"search payload":         `{"query":"x","results":[]}`,
		"service status started": `{"status":"started","name":"api"}`,
		"kind but bad status":    `{"kind":"x","status":"weird"}`,
		"status but no kind":     `{"status":"ok"}`,
		"web_fetch old shape":    `{"status":"HTTP 200","content":"page"}`,
	}
	for name, s := range reject {
		if _, ok := ParseToolMessage(s); ok {
			t.Errorf("%s: should be rejected, was accepted: %q", name, s)
		}
	}
	for _, s := range []string{
		`{"kind":"page","status":"ok","content":"hi"}`,
		`{"kind":"search","status":"empty","detail":"none"}`,
	} {
		if _, ok := ParseToolMessage(s); !ok {
			t.Errorf("valid envelope rejected: %q", s)
		}
	}
}
