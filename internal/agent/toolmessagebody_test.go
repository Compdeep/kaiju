package agent

import (
	"strings"
	"testing"

	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
)

func tmBody(m agenttools.ToolMessage) toolMessageBody { return toolMessageBody{msg: m} }

func TestToolMessageBody_FieldFromData(t *testing.T) {
	b := tmBody(agenttools.ToolOK("page", "text", map[string]any{"title": "T", "n": 3}))
	if v, ok := b.Field("title"); !ok || v != "T" {
		t.Fatalf("Field(title) = %v,%v want T,true", v, ok)
	}
	if v, ok := b.Field("data.title"); !ok || v != "T" {
		t.Fatalf("Field(data.title) should also resolve: %v,%v", v, ok)
	}
	if _, ok := b.Field("missing"); ok {
		t.Fatalf("Field(missing) should miss")
	}
}

// A text tool carries its whole result in Content with no Data; field access
// must still reach into it when it happens to be JSON (a JSON file, a JSON kv).
func TestToolMessageBody_FieldFallsBackToContent(t *testing.T) {
	b := tmBody(agenttools.ToolOK("kv", `{"port":8080}`, nil))
	if v, ok := b.Field("port"); !ok || v != float64(8080) {
		t.Fatalf("Field(port) via content fallback = %v,%v want 8080,true", v, ok)
	}
}

func TestToolMessageBody_Evidence(t *testing.T) {
	if got := tmBody(agenttools.ToolOK("page", "the page", nil)).Evidence(); got != "the page" {
		t.Fatalf("ok Evidence = %q", got)
	}
	if got := tmBody(agenttools.ToolEmpty("search", "no results")).Evidence(); got != "(no search: no results)" {
		t.Fatalf("empty Evidence = %q want explicit absence line", got)
	}
	if got := tmBody(agenttools.ToolFail("page", "HTTP 404", nil)).Evidence(); got != "(page failed: HTTP 404)" {
		t.Fatalf("error Evidence = %q want explicit failure line", got)
	}
	// ok + no content → render the data payload rather than empty
	if got := tmBody(agenttools.ToolOK("sysinfo", "", map[string]any{"os": "linux"})).Evidence(); !strings.Contains(got, "linux") {
		t.Fatalf("ok+no-content Evidence should render data, got %q", got)
	}
}

func TestToolMessageBody_Summary(t *testing.T) {
	if got := tmBody(agenttools.ToolEmpty("search", "no results")).Summary(); !strings.HasPrefix(got, "search empty") || !strings.Contains(got, "no results") {
		t.Fatalf("empty Summary = %q", got)
	}
	if got := tmBody(agenttools.ToolOK("page", "x", nil)).Summary(); got != "page ok" {
		t.Fatalf("ok Summary = %q want 'page ok'", got)
	}
}

func TestRawTextBody(t *testing.T) {
	b := RawText("plain text\nsecond line")
	if _, ok := b.Field("x"); ok {
		t.Fatalf("RawText.Field on non-JSON should miss")
	}
	if b.Evidence() != "plain text\nsecond line" {
		t.Fatalf("RawText.Evidence should be the text verbatim")
	}
	if b.Summary() != "plain text" {
		t.Fatalf("RawText.Summary should be the first non-empty line, got %q", b.Summary())
	}
	j := RawText(`{"a":{"b":5}}`)
	if v, ok := j.Field("a.b"); !ok || v != float64(5) {
		t.Fatalf("RawText.Field JSON dot-path = %v,%v want 5,true", v, ok)
	}
}
