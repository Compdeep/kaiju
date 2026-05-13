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
	err := validateDirectParams(tool, map[string]any{"command": "ls"})
	if err != nil {
		t.Fatalf("expected allow, got %v", err)
	}
}

func TestValidateDirectParams_RejectsUndeclared_WhenAdditionalFalse(t *testing.T) {
	tool := &fakeTool{
		name:   "bash",
		params: json.RawMessage(`{"properties":{"command":{},"timeout_sec":{}},"additionalProperties":false}`),
	}
	err := validateDirectParams(tool, map[string]any{"command": "ls", "cwd": "/tmp"})
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
	})
	if err != nil {
		t.Fatalf("expected allow (additionalProperties default=true), got %v", err)
	}
}

func TestValidateDirectParams_RejectsMalformedSchema(t *testing.T) {
	tool := &fakeTool{name: "broken", params: json.RawMessage(`garbage`)}
	err := validateDirectParams(tool, map[string]any{"x": 1})
	if err == nil {
		t.Fatalf("expected error for malformed schema")
	}
}

// ── validateDataFlow (template-aware) ───────────────────────────────────

func TestValidateDataFlow_AllowsEmptyDependsOn(t *testing.T) {
	if err := validateDataFlow("compute", nil, nil); err != nil {
		t.Fatalf("no deps → should allow, got %v", err)
	}
}

func TestValidateDataFlow_AllowsWhenTemplatePresent(t *testing.T) {
	params := map[string]any{"context.x": "${node.n1.content}"}
	if err := validateDataFlow("compute", []string{"n1"}, params); err != nil {
		t.Fatalf("deps + template → should allow, got %v", err)
	}
}

func TestValidateDataFlow_AllowsStepFormTemplate(t *testing.T) {
	// Pre-rewrite (planStepsToNodes) the template still uses ${step.N}.
	// Validator should accept either form so the check survives both
	// stages of the pipeline if it ever fires earlier than expected.
	params := map[string]any{"context.x": "${step.0.content}"}
	if err := validateDataFlow("compute", []string{"n1"}, params); err != nil {
		t.Fatalf("step.N templates should also count as wired, got %v", err)
	}
}

func TestValidateDataFlow_AllowsBashWithDepsNoTemplate(t *testing.T) {
	// Pure sequencing without data flow is legitimate for bash/service.
	if err := validateDataFlow("bash", []string{"n1"}, map[string]any{"command": "ls"}); err != nil {
		t.Fatalf("bash sequencing should not be rejected, got %v", err)
	}
}

func TestValidateDataFlow_AllowsServiceWithDepsNoTemplate(t *testing.T) {
	if err := validateDataFlow("service", []string{"n1", "n2"}, nil); err != nil {
		t.Fatalf("service sequencing should not be rejected, got %v", err)
	}
}

func TestValidateDataFlow_RejectsComputeWithDepsNoTemplate(t *testing.T) {
	params := map[string]any{"goal": "rank stuff", "mode": "shallow"}
	err := validateDataFlow("compute", []string{"n1", "n2", "n3"}, params)
	if err == nil {
		t.Fatalf("compute with deps but no template → should reject")
	}
	if !strings.Contains(err.Error(), "compute") {
		t.Fatalf("error should name the tool, got %v", err)
	}
	if !strings.Contains(err.Error(), "${step.N") {
		t.Fatalf("error should hint at template syntax, got %v", err)
	}
}

func TestValidateDataFlow_AllowsEditFileWithTaskFiles(t *testing.T) {
	// edit_file reads its files via task_files — that IS the data source.
	// depends_on against an upstream file_read is just sequencing, not a
	// data-flow omission.
	err := validateDataFlow("edit_file", []string{"n0"}, map[string]any{"goal": "fix it", "task_files": []string{"project/foo.py"}})
	if err != nil {
		t.Fatalf("edit_file with task_files should be allowed regardless of templates, got %v", err)
	}
}

func TestValidateDataFlow_RejectsEditFileWithoutTaskFiles(t *testing.T) {
	// Defensive: if somehow edit_file is called with deps but no task_files,
	// the rule still fires (no data source declared, no template either).
	err := validateDataFlow("edit_file", []string{"n0"}, map[string]any{"goal": "fix it"})
	if err == nil {
		t.Fatalf("edit_file with deps but no task_files and no template → should reject")
	}
}

func TestValidateDataFlow_AllowsEditFileWithTaskFilesAnyShape(t *testing.T) {
	// JSON-decoded planner output uses []any, not []string. Both must work.
	err := validateDataFlow("edit_file", []string{"n0"}, map[string]any{"goal": "fix it", "task_files": []any{"project/foo.py"}})
	if err != nil {
		t.Fatalf("edit_file with []any task_files should be allowed, got %v", err)
	}
}

func TestValidateDataFlow_RejectsEmptyParams(t *testing.T) {
	// Explicitly-empty map is the same as nil — both mean "no wiring".
	err := validateDataFlow("compute", []string{"n1"}, map[string]any{})
	if err == nil {
		t.Fatalf("empty map should be treated the same as nil")
	}
}

func TestValidateDataFlow_FindsTemplateInNestedValue(t *testing.T) {
	// Templates can be nested inside arrays or sub-objects (e.g. compute
	// params with a context dict). Walker must reach them.
	params := map[string]any{
		"goal": "compose",
		"items": []any{
			map[string]any{"src": "${node.n1.content}"},
		},
	}
	if err := validateDataFlow("compute", []string{"n1"}, params); err != nil {
		t.Fatalf("nested template should count as wired, got %v", err)
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

// errorsIs kept as a tiny utility — used to live alongside an
// errResultNotJSON sentinel that's since been deleted. Left here in
// case future tests want to chase wrapped error chains without pulling
// in "errors" at the file level.
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
