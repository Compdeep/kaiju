package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvelopeSchema(t *testing.T) {
	bare := EnvelopeSchema("")
	if !json.Valid(bare) {
		t.Fatalf("EnvelopeSchema(\"\") is not valid JSON: %s", bare)
	}
	if !strings.Contains(string(bare), `"enum":["ok","empty","error"]`) {
		t.Fatalf("envelope schema must carry the status enum: %s", bare)
	}
	if strings.Contains(string(bare), `"data"`) {
		t.Fatalf("EnvelopeSchema(\"\") should omit data")
	}
	withData := EnvelopeSchema(`{"type":"array"}`)
	if !json.Valid(withData) || !strings.Contains(string(withData), `"data":{"type":"array"}`) {
		t.Fatalf("EnvelopeSchema(data) should slot the data schema in: %s", withData)
	}
}

func TestToolMessage_Constructors(t *testing.T) {
	ok := ToolOK("page", "hello", map[string]any{"x": 1})
	if ok.Type != "page" || ok.Status != StatusOK || ok.Content != "hello" {
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
	if txt.Type != "text" || txt.Status != StatusOK || txt.Content != "just prose" || txt.Data != nil {
		t.Fatalf("ToolText wrong: %+v", txt)
	}
}

func TestToolMessage_JSONRoundTrip(t *testing.T) {
	orig := ToolOK("listing", "", map[string]any{"entries": []string{"a", "b"}})
	got, ok := ParseToolMessage(orig.JSON())
	if !ok {
		t.Fatalf("round-trip: ParseToolMessage failed on %s", orig.JSON())
	}
	if got.Type != orig.Type || got.Status != orig.Status {
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

// The coupled tools (bash, debug) are deliberately NOT migrated — their current
// output is read by name by consumers (isBashError's "bash_error":true match,
// the debug graft's type=="debug"). This asserts their real output shapes are
// NOT mistaken for an envelope, so they stay RawTextBody and those consumers
// keep working. If this ever fails, migrating bash/debug silently broke the
// failure→Holmes loop.
func TestParseToolMessage_CoupledToolsStayRaw(t *testing.T) {
	coupled := map[string]string{
		"bash success (raw text)": "total 8\ndrwxr-xr-x  2 u u 4096 .\n",
		"bash error JSON":         `{"bash_error":true,"exit_code":1,"stdout":"","stderr":"boom","error":"exit status 1","command":"false"}`,
		"debug envelope":          `{"type":"debug","problem":"nil deref at x.go:3"}`,
	}
	for name, s := range coupled {
		if _, ok := ParseToolMessage(s); ok {
			t.Errorf("%s: must NOT be wrapped as an envelope (it is read raw by a consumer): %q", name, s)
		}
	}
}

// service is a coupled migration: its lifecycle status moved into Data, and the
// scheduler's health-check graft must read it from there. This asserts that
// exact path: envelope status is "ok", and status/name/port are recoverable
// from Data — if it breaks, the post-start health check silently stops grafting.
func TestServiceEnvelope_GraftReadsStatusFromData(t *testing.T) {
	out := ToolOK("service", "", map[string]any{"status": "started", "name": "api", "pid": 42, "port": 4000}).JSON()
	msg, ok := ParseToolMessage(out)
	if !ok {
		t.Fatalf("service output should be a valid envelope: %s", out)
	}
	if msg.Status != StatusOK {
		t.Fatalf("envelope status = %q, want ok (lifecycle status lives in Data)", msg.Status)
	}
	var svc struct {
		Status string `json:"status"`
		Name   string `json:"name"`
		Port   int    `json:"port"`
	}
	if err := json.Unmarshal(msg.Data, &svc); err != nil {
		t.Fatalf("health-check graft can't read service Data: %v", err)
	}
	if svc.Status != "started" || svc.Name != "api" || svc.Port != 4000 {
		t.Fatalf("graft read wrong service data: %+v", svc)
	}
}

// The field was "kind" before it was renamed to "type". A remote plugin host is
// third-party code built against whichever name it saw, so reading accepts both
// and writing always uses the new one.
func TestParseToolMessage_AcceptsTheOldFieldName(t *testing.T) {
	m, ok := ParseToolMessage(`{"kind":"page","status":"ok","content":"hello"}`)
	if !ok {
		t.Fatal("an envelope using the old field name must still be recognised")
	}
	if m.Type != "page" || m.Content != "hello" {
		t.Fatalf("got %+v", m)
	}
	// Writing it back uses the new name only.
	if got := m.JSON(); !strings.Contains(got, `"type":"page"`) || strings.Contains(got, `"kind"`) {
		t.Fatalf("output should carry only the new name, got %s", got)
	}
}

// The new name wins when a producer somehow sends both.
func TestParseToolMessage_NewNameWins(t *testing.T) {
	m, ok := ParseToolMessage(`{"type":"listing","kind":"page","status":"ok"}`)
	if !ok || m.Type != "listing" {
		t.Fatalf("got (%+v, %v), want listing", m, ok)
	}
}
