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
