package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/internal/agent/tools"
)

// fakeTool implements tools.Tool with caller-specified Parameters JSON,
// so each test can express a precise schema shape without standing up a
// real registry entry.
type fakeTool struct {
	name   string
	params json.RawMessage
}

func (f *fakeTool) Name() string                             { return f.name }
func (f *fakeTool) Description() string                      { return "" }
func (f *fakeTool) Parameters() json.RawMessage              { return f.params }
func (f *fakeTool) Impact(map[string]any) int                { return 0 }
func (f *fakeTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}

var _ tools.Tool = (*fakeTool)(nil)

// ── parseToolSchema ──────────────────────────────────────────────────────

func TestParseToolSchema_DefaultsAdditionalToTrue(t *testing.T) {
	s, err := parseToolSchema(json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`))
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !s.AdditionalProperties {
		t.Fatalf("expected AdditionalProperties=true when field is absent, got false")
	}
	if _, ok := s.Properties["a"]; !ok {
		t.Fatalf("expected property a to be parsed")
	}
}

func TestParseToolSchema_ReadsExplicitFalse(t *testing.T) {
	s, _ := parseToolSchema(json.RawMessage(`{"properties":{"a":{}},"additionalProperties":false}`))
	if s.AdditionalProperties {
		t.Fatalf("expected AdditionalProperties=false, got true")
	}
}

func TestParseToolSchema_ReadsExplicitTrue(t *testing.T) {
	s, _ := parseToolSchema(json.RawMessage(`{"properties":{"a":{}},"additionalProperties":true}`))
	if !s.AdditionalProperties {
		t.Fatalf("expected AdditionalProperties=true, got false")
	}
}

func TestParseToolSchema_ObjectFormAdditionalFallsBackToTrue(t *testing.T) {
	// Object-form additionalProperties (sub-schema) isn't supported by
	// this validator — we fall back to the lenient default so we don't
	// spuriously reject legitimate tool calls.
	s, _ := parseToolSchema(json.RawMessage(`{"properties":{},"additionalProperties":{"type":"string"}}`))
	if !s.AdditionalProperties {
		t.Fatalf("expected object-form additionalProperties to fall through as true")
	}
}

func TestParseToolSchema_MalformedJSONReturnsError(t *testing.T) {
	_, err := parseToolSchema(json.RawMessage(`not json`))
	if err == nil {
		t.Fatalf("expected error on malformed schema")
	}
}

func TestParseToolSchema_EmptyRawIsLenient(t *testing.T) {
	s, err := parseToolSchema(nil)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !s.AdditionalProperties {
		t.Fatalf("expected empty schema to default to additionalProperties=true")
	}
}

// ── validateDirectParams ─────────────────────────────────────────────────

func TestValidateDirectParams_AllowsDeclaredKeys(t *testing.T) {
	tool := &fakeTool{
		name:   "bash",
		params: json.RawMessage(`{"properties":{"command":{},"timeout_sec":{}},"additionalProperties":false}`),
	}
	err := validateDirectParams(tool, map[string]any{"command": "ls"}, nil)
	if err != nil {
		t.Fatalf("expected allow, got %v", err)
	}
}

func TestValidateDirectParams_RejectsUndeclared_WhenAdditionalFalse(t *testing.T) {
	tool := &fakeTool{
		name:   "bash",
		params: json.RawMessage(`{"properties":{"command":{},"timeout_sec":{}},"additionalProperties":false}`),
	}
	err := validateDirectParams(tool, map[string]any{"command": "ls", "cwd": "/tmp"}, nil)
	if err == nil {
		t.Fatalf("expected reject of unknown param `cwd`")
	}
	if !strings.Contains(err.Error(), "cwd") {
		t.Fatalf("expected error to name the offending param, got %v", err)
	}
	if !strings.Contains(err.Error(), "command") {
		t.Fatalf("expected error to list allowed params, got %v", err)
	}
}

func TestValidateDirectParams_AllowsUndeclared_WhenAdditionalAbsent(t *testing.T) {
	// Compute's stance: no additionalProperties field → default true → extras allowed.
	tool := &fakeTool{
		name:   "compute",
		params: json.RawMessage(`{"properties":{"goal":{},"mode":{}}}`),
	}
	err := validateDirectParams(tool, map[string]any{
		"goal":       "X",
		"mode":       "shallow",
		"ports_data": "some data",
	}, nil)
	if err != nil {
		t.Fatalf("expected allow (additionalProperties default=true), got %v", err)
	}
}

func TestValidateDirectParams_IgnoresKeysCoveredByParamRefs(t *testing.T) {
	// When a param's value is injected via param_refs, its name is the
	// planner's choice and not meant to appear in the tool's schema.
	tool := &fakeTool{
		name:   "file_write",
		params: json.RawMessage(`{"properties":{"path":{},"content":{}},"additionalProperties":false}`),
	}
	refs := map[string]ResolvedInjection{
		"content": {NodeID: "n1", Field: "body"},
	}
	err := validateDirectParams(tool, map[string]any{"path": "/tmp/x", "content": "ignored-by-refs"}, refs)
	if err != nil {
		t.Fatalf("content comes from refs, should be allowed: %v", err)
	}
}

func TestValidateDirectParams_RejectsMalformedSchema(t *testing.T) {
	tool := &fakeTool{name: "broken", params: json.RawMessage(`garbage`)}
	err := validateDirectParams(tool, map[string]any{"x": 1}, nil)
	if err == nil {
		t.Fatalf("expected error for malformed schema")
	}
}

// ── validateParamRef ─────────────────────────────────────────────────────

func TestValidateParamRef_AcceptsFieldPresent(t *testing.T) {
	ref := ResolvedInjection{NodeID: "n1", Field: "output"}
	depResult := `{"output":"42","other":"x"}`
	if err := validateParamRef("content", ref, depResult); err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}

func TestValidateParamRef_RejectsFieldAbsent(t *testing.T) {
	ref := ResolvedInjection{NodeID: "n1", Field: "output"}
	depResult := `{"files_created":["x"],"type":"result"}`
	err := validateParamRef("content", ref, depResult)
	if err == nil {
		t.Fatalf("expected reject for missing field")
	}
	if !strings.Contains(err.Error(), "output") || !strings.Contains(err.Error(), "n1") {
		t.Fatalf("error should name field and dep, got %v", err)
	}
}

func TestValidateParamRef_AcceptsEmptyFieldAsFullResult(t *testing.T) {
	// Planner choosing field="" means "inject the whole result as-is".
	// Legitimate for chaining raw text like file_read output.
	ref := ResolvedInjection{NodeID: "n1", Field: ""}
	if err := validateParamRef("content", ref, "some raw text"); err != nil {
		t.Fatalf("empty field should be allowed: %v", err)
	}
}

func TestValidateParamRef_RejectsMalformedDepResult(t *testing.T) {
	ref := ResolvedInjection{NodeID: "n1", Field: "output"}
	err := validateParamRef("content", ref, "not json at all")
	if err == nil {
		t.Fatalf("expected reject when dep result is not JSON and field is specified")
	}
}

// ── validateDataFlow ─────────────────────────────────────────────────────

func TestValidateDataFlow_AllowsEmptyDependsOn(t *testing.T) {
	if err := validateDataFlow("compute", nil, nil); err != nil {
		t.Fatalf("no deps → should allow, got %v", err)
	}
}

func TestValidateDataFlow_AllowsWhenParamRefsPresent(t *testing.T) {
	refs := map[string]ResolvedInjection{"ctx": {NodeID: "n1", Field: "output"}}
	if err := validateDataFlow("compute", []string{"n1"}, refs); err != nil {
		t.Fatalf("deps + refs → should allow, got %v", err)
	}
}

func TestValidateDataFlow_AllowsBashWithDepsNoRefs(t *testing.T) {
	// Pure sequencing without data flow is legitimate for bash/service/etc.
	if err := validateDataFlow("bash", []string{"n1"}, nil); err != nil {
		t.Fatalf("bash sequencing should not be rejected, got %v", err)
	}
}

func TestValidateDataFlow_AllowsServiceWithDepsNoRefs(t *testing.T) {
	if err := validateDataFlow("service", []string{"n1", "n2"}, nil); err != nil {
		t.Fatalf("service sequencing should not be rejected, got %v", err)
	}
}

func TestValidateDataFlow_RejectsComputeWithDepsNoRefs(t *testing.T) {
	err := validateDataFlow("compute", []string{"n1", "n2", "n3"}, nil)
	if err == nil {
		t.Fatalf("compute with deps but no refs → should reject")
	}
	if !strings.Contains(err.Error(), "compute") {
		t.Fatalf("error should name the tool, got %v", err)
	}
	if !strings.Contains(err.Error(), "param_refs") {
		t.Fatalf("error should suggest param_refs, got %v", err)
	}
}

func TestValidateDataFlow_RejectsEditFileWithDepsNoRefs(t *testing.T) {
	err := validateDataFlow("edit_file", []string{"n0"}, nil)
	if err == nil {
		t.Fatalf("edit_file with deps but no refs → should reject")
	}
	if !strings.Contains(err.Error(), "edit_file") {
		t.Fatalf("error should name the tool, got %v", err)
	}
}

func TestValidateDataFlow_RejectsEmptyRefsMap(t *testing.T) {
	// An explicitly-empty map is the same as nil — both mean "no wiring".
	err := validateDataFlow("compute", []string{"n1"}, map[string]ResolvedInjection{})
	if err == nil {
		t.Fatalf("empty map should be treated the same as nil")
	}
}

// ── validateParamRef two-case split ──────────────────────────────────────

func TestValidateParamRef_NonJSON_ReturnsSentinel(t *testing.T) {
	// A dep whose Result is plain text (not JSON) should not be treated as
	// a planner error — it's an upstream tool bug. Return errResultNotJSON
	// so the caller can degrade to full-result.
	ref := ResolvedInjection{NodeID: "n1", Field: "content"}
	err := validateParamRef("body", ref, "plain text, not json")
	if err == nil {
		t.Fatalf("expected sentinel error")
	}
	if !errorsIs(err, errResultNotJSON) {
		t.Fatalf("expected errResultNotJSON sentinel, got %v", err)
	}
}

func TestValidateParamRef_ValidJSONMissingField_ReturnsHardError(t *testing.T) {
	// Envelope parses but the field isn't in it — brace_json shape. Must
	// be distinguishable from the non-JSON case.
	ref := ResolvedInjection{NodeID: "n1", Field: "output"}
	err := validateParamRef("body", ref, `{"files_created":["x"],"type":"result"}`)
	if err == nil {
		t.Fatalf("expected hard error for missing field in valid JSON")
	}
	if errorsIs(err, errResultNotJSON) {
		t.Fatalf("should NOT return the non-JSON sentinel when JSON is valid")
	}
}

// ── truncateToolResult ──────────────────────────────────────────────────

func TestTruncateToolResult_UnchangedWhenUnderCap(t *testing.T) {
	in := `{"status":"200","content":"hello"}`
	out := truncateToolResult(in, 1000, fakeHeadTail)
	if out != in {
		t.Fatalf("small result should pass through unchanged")
	}
}

func TestTruncateToolResult_PreservesJSONValidity(t *testing.T) {
	// Build a web_fetch-shaped result with a content string that's too
	// big. Truncation should shrink content but keep the envelope valid.
	big := strings.Repeat("abc\n", 2000) // 8000 chars, contains newlines
	raw, _ := json.Marshal(map[string]any{
		"status":  "200",
		"content": big,
		"format":  "markdown",
	})
	out := truncateToolResult(string(raw), 1024, fakeHeadTail)
	if len(out) > 1200 {
		t.Fatalf("truncated result still too big: %d bytes", len(out))
	}
	var check map[string]any
	if err := json.Unmarshal([]byte(out), &check); err != nil {
		t.Fatalf("truncated result is not valid JSON: %v — %s", err, out)
	}
	// Envelope fields survive.
	if check["status"] != "200" || check["format"] != "markdown" {
		t.Fatalf("non-content fields should survive: %v", check)
	}
}

func TestTruncateToolResult_NonJSONFallsBackToHeadTail(t *testing.T) {
	// Plain text output of a bash tool — can't be shrunk semantically,
	// falls back to head+tail splice.
	big := strings.Repeat("x", 5000)
	out := truncateToolResult(big, 1024, fakeHeadTail)
	if len(out) > 1200 {
		t.Fatalf("non-JSON result not shrunk: %d bytes", len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncation marker in output")
	}
}

// fakeHeadTail mimics Text.HeadTail for test use without importing it.
func fakeHeadTail(s string, headN, tailN int, sep ...string) string {
	marker := "... (truncated) ..."
	if len(sep) > 0 {
		marker = sep[0]
	}
	if len(s) <= headN+tailN+len(marker) {
		return s
	}
	return s[:headN] + marker + s[len(s)-tailN:]
}

// errorsIs is a tiny wrapper to avoid pulling "errors" into the test
// file's imports just for this one use.
func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
