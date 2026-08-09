package agent

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

func tmBody(m toolapi.ToolMessage) toolMessageBody { return toolMessageBody{msg: m} }

func TestToolMessageBody_FieldFromData(t *testing.T) {
	b := tmBody(toolapi.ToolOK("page", "text", map[string]any{"title": "T", "n": 3}))
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

// A bare "data" reference names the payload wrapper itself — it must resolve to
// the WHOLE payload, not look for a field literally named "data". This is the
// exact case that hard-failed a live run:
//
//	dependency injection failed: template on n6: field "data" absent in dep n3
//
// where n3 was sysinfo (structured payload, no field named "data"). The dotted
// form ("data.title") was already tolerated; the bare form was the gap.
// Plan-time validation already accepts ${step.N.data}, so runtime was the outlier.
func TestToolMessageBody_FieldBareData(t *testing.T) {
	b := tmBody(toolapi.ToolOK("sysinfo", "", map[string]any{"os": "linux", "cpus": 8}))
	v, ok := b.Field("data")
	if !ok {
		t.Fatalf(`Field("data") should resolve to the whole payload, got ok=false`)
	}
	m, isMap := v.(map[string]any)
	if !isMap || m["cpus"] != float64(8) {
		t.Fatalf(`Field("data") = %#v want the payload map with cpus=8`, v)
	}
	// Dotted and payload-relative forms remain intact.
	if v, ok := b.Field("data.cpus"); !ok || v != float64(8) {
		t.Fatalf(`Field("data.cpus") = %v,%v want 8,true`, v, ok)
	}
	if v, ok := b.Field("cpus"); !ok || v != float64(8) {
		t.Fatalf(`Field("cpus") = %v,%v want 8,true`, v, ok)
	}
	// We tolerate the wrapper, not everything — a genuinely absent field still misses.
	if _, ok := b.Field("nonexistent"); ok {
		t.Fatalf(`Field("nonexistent") should still miss`)
	}
}

// EnvelopeSchema tells the planner a result has content, detail, status and
// type, so a plan may reference them. Before these resolved, ${node.X.content}
// passed plan-time validation and then failed at fire time, and a tool whose
// readable half is Content with counts in Data had no working path to its text.
func TestToolMessageBody_FieldEnvelopeNames(t *testing.T) {
	b := tmBody(toolapi.ToolOK("listing", "two rows", map[string]any{"count": 2}))
	if v, ok := b.Field("content"); !ok || v != "two rows" {
		t.Fatalf("Field(content) = %v,%v want the rendered text", v, ok)
	}
	if v, ok := b.Field("status"); !ok || v != "ok" {
		t.Fatalf("Field(status) = %v,%v want ok", v, ok)
	}
	if v, ok := b.Field("type"); !ok || v != "listing" {
		t.Fatalf("Field(type) = %v,%v want listing", v, ok)
	}
	e := tmBody(toolapi.ToolEmpty("listing", "no incident matches status open"))
	if v, ok := e.Field("detail"); !ok || v != "no incident matches status open" {
		t.Fatalf("Field(detail) = %v,%v want the reason", v, ok)
	}
	// Where both have the name the payload wins, in either form: web_fetch's
	// payload carries the HTTP status and ${node.X.status} has always meant it.
	c := tmBody(toolapi.ToolOK("page", "rendered", map[string]any{"content": "raw", "status": "200 OK"}))
	if v, ok := c.Field("content"); !ok || v != "raw" {
		t.Fatalf("Field(content) = %v,%v want the payload's own key", v, ok)
	}
	if v, ok := c.Field("data.content"); !ok || v != "raw" {
		t.Fatalf("Field(data.content) = %v,%v want the payload's own key", v, ok)
	}
	if v, ok := c.Field("status"); !ok || v != "200 OK" {
		t.Fatalf("Field(status) = %v,%v want the payload's HTTP status", v, ok)
	}
}

// A text tool carries its whole result in Content with no Data; field access
// must still reach into it when it happens to be JSON (a JSON file, a JSON kv).
func TestToolMessageBody_FieldFallsBackToContent(t *testing.T) {
	b := tmBody(toolapi.ToolOK("kv", `{"port":8080}`, nil))
	if v, ok := b.Field("port"); !ok || v != float64(8080) {
		t.Fatalf("Field(port) via content fallback = %v,%v want 8080,true", v, ok)
	}
}

// A command that ran and exited non-zero usually printed the only thing that
// says why. Keeping it in the payload made it survive for templates and the
// frontend, and the model still read the exit status alone.
func TestToolMessageBody_EvidenceKeepsWhatAFailedToolProduced(t *testing.T) {
	b := tmBody(toolapi.ToolFail("command", "exit status 1",
		map[string]any{"output": "cat: /root/x: Permission denied"}))
	got := b.Evidence()
	if !strings.Contains(got, "command failed: exit status 1") {
		t.Errorf("Evidence should still say what failed, got %q", got)
	}
	if !strings.Contains(got, "Permission denied") {
		t.Errorf("Evidence dropped the output that says why:\n%s", got)
	}
	// A tool that could not start produces neither, and gets the line alone.
	if got := tmBody(toolapi.ToolFail("page", "HTTP 404", nil)).Evidence(); got != "(page failed: HTTP 404)" {
		t.Errorf("a failure with no output = %q, want the line alone", got)
	}
}

func TestToolMessageBody_Evidence(t *testing.T) {
	if got := tmBody(toolapi.ToolOK("page", "the page", nil)).Evidence(); got != "the page" {
		t.Fatalf("ok Evidence = %q", got)
	}
	if got := tmBody(toolapi.ToolEmpty("search", "no results")).Evidence(); got != "(no search: no results)" {
		t.Fatalf("empty Evidence = %q want explicit absence line", got)
	}
	if got := tmBody(toolapi.ToolFail("page", "HTTP 404", nil)).Evidence(); got != "(page failed: HTTP 404)" {
		t.Fatalf("error Evidence = %q want explicit failure line", got)
	}
	// ok + no content → render the data payload rather than empty
	if got := tmBody(toolapi.ToolOK("sysinfo", "", map[string]any{"os": "linux"})).Evidence(); !strings.Contains(got, "linux") {
		t.Fatalf("ok+no-content Evidence should render data, got %q", got)
	}
}

func TestToolMessageBody_Summary(t *testing.T) {
	if got := tmBody(toolapi.ToolEmpty("search", "no results")).Summary(); !strings.HasPrefix(got, "search empty") || !strings.Contains(got, "no results") {
		t.Fatalf("empty Summary = %q", got)
	}
	if got := tmBody(toolapi.ToolOK("page", "x", nil)).Summary(); got != "page ok" {
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
