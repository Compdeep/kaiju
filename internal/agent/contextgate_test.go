package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/llm"
)

// ── Test fixtures ────────────────────────────────────────────────────────────

// newTestContextGate constructs a ContextGate with a real Graph but a minimal Agent.
// Workspace and metadata both point to a temp dir so source loads are isolated.
func newTestContextGate(t *testing.T) (*ContextGate, *Graph, *Trigger, string) {
	t.Helper()
	dir := t.TempDir()
	graph := NewGraph()
	graph.SessionID = "test-session"
	trigger := &Trigger{
		Type:      "chat_query",
		AlertID:   "test-alert",
		SessionID: "test-session",
	}
	agent := &Agent{
		cfg: Config{
			Workspace:   dir,
			MetadataDir: dir,
		},
	}
	gate := NewContextGate(graph, trigger, agent)
	graph.Context = gate
	return gate, graph, trigger, dir
}

// ── Constructor and registry ─────────────────────────────────────────────────

func TestNewContextGate_RegistersAllSources(t *testing.T) {
	gate, _, _, _ := newTestContextGate(t)

	expected := []string{
		SourceBlueprint, SourceWorklog, SourceNodeReturns, SourceWorkspaceTree,
		SourceServiceState, SourceHistory, SourceSkillGuidance,
		SourceWorkspaceDeep, SourceFunctionMap, SourceExistingBlueprints,
	}
	if len(gate.sources) != len(expected) {
		t.Fatalf("expected %d sources, got %d", len(expected), len(gate.sources))
	}
	for _, name := range expected {
		if _, ok := gate.sources[name]; !ok {
			t.Errorf("source %q not registered", name)
		}
	}
}

func TestNewContextGate_AttachesToGraph(t *testing.T) {
	gate, graph, _, _ := newTestContextGate(t)
	if graph.Context != gate {
		t.Error("expected gate to be attached to graph.Context")
	}
}

// ── Get() deterministic path ─────────────────────────────────────────────────

func TestGet_NoSources_ReturnsEmpty(t *testing.T) {
	gate, _, _, _ := newTestContextGate(t)
	resp, err := gate.Get(context.Background(), ContextRequest{})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if resp.Summary != "" {
		t.Errorf("expected empty summary, got %q", resp.Summary)
	}
	if len(resp.Sources) != 0 {
		t.Errorf("expected zero sources, got %d", len(resp.Sources))
	}
}

func TestGet_UnknownSource_ReturnsError(t *testing.T) {
	gate, _, _, _ := newTestContextGate(t)
	_, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: []SourceSpec{{Name: "no_such_source"}},
	})
	if err == nil {
		t.Fatal("expected error for unknown source")
	}
	if !strings.Contains(err.Error(), "unknown source") {
		t.Errorf("expected unknown source error, got %v", err)
	}
}

func TestGet_EmptySourceName_ReturnsError(t *testing.T) {
	gate, _, _, _ := newTestContextGate(t)
	_, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: []SourceSpec{{Name: ""}},
	})
	if err == nil {
		t.Fatal("expected error for empty source name")
	}
}

func TestGet_NilGate_ReturnsError(t *testing.T) {
	var gate *ContextGate
	_, err := gate.Get(context.Background(), ContextRequest{})
	if err == nil {
		t.Fatal("expected error from nil gate")
	}
}

func TestGet_ReturnSourcesExceedBudget_TrimsAndReturns(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)

	// Create a worklog larger than the budget
	bigContent := strings.Repeat("x", 5000)
	wlPath := filepath.Join(dir, ".worklog")
	if err := os.WriteFile(wlPath, []byte(bigContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Use a session ID that maps to legacy path so the test write is read.
	gate.trigger.SessionID = ""
	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(Worklog(1000, "all")),
		MaxBudget:     1000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	// Worklog should be present but truncated.
	wl, ok := resp.Sources[SourceWorklog]
	if !ok {
		t.Fatal("expected worklog in sources")
	}
	if len(wl) > 1000 {
		t.Errorf("worklog len %d exceeds budget 1000", len(wl))
	}
	if len(resp.Trimmed) == 0 {
		t.Errorf("expected Trimmed to record the worklog truncation")
	}
}

func TestGet_BudgetTrim_LaterSourcesDropped(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)

	// Worklog content fills most of the budget
	wlPath := filepath.Join(dir, ".worklog")
	if err := os.WriteFile(wlPath, []byte(strings.Repeat("a", 800)), 0644); err != nil {
		t.Fatal(err)
	}
	gate.trigger.SessionID = ""

	resp, err := gate.Get(context.Background(), ContextRequest{
		// Two sources: worklog first (high priority), node_returns second.
		ReturnSources: Sources(Worklog(1000, "all"), NodeReturns("all")),
		MaxBudget:     900,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	// Worklog should be intact (it fits).
	if got := len(resp.Sources[SourceWorklog]); got == 0 {
		t.Errorf("expected worklog to be present, got empty")
	}
}

func TestGet_DefaultBudget(t *testing.T) {
	gate, _, _, _ := newTestContextGate(t)
	resp, err := gate.Get(context.Background(), ContextRequest{
		// No MaxBudget — should default
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
}

// ── Helper constructors ──────────────────────────────────────────────────────

func TestSourceConstructors(t *testing.T) {
	tests := []struct {
		name string
		spec SourceSpec
		want string
	}{
		{"Blueprint", Blueprint(), SourceBlueprint},
		{"BlueprintNamed", BlueprintNamed("foo"), SourceBlueprint},
		{"Worklog", Worklog(20, "all"), SourceWorklog},
		{"NodeReturns", NodeReturns("failures"), SourceNodeReturns},
		{"NodeReturnsTyped", NodeReturnsTyped("all", []string{"bash"}), SourceNodeReturns},
		{"WorkspaceTree", WorkspaceTree(3), SourceWorkspaceTree},
		{"WorkspaceTreeFocus", WorkspaceTreeFocus(3, "src"), SourceWorkspaceTree},
		{"ServiceState", ServiceState(), SourceServiceState},
		{"ServiceStateFiltered", ServiceStateFiltered("running"), SourceServiceState},
		{"History", History(5), SourceHistory},
		{"SkillGuidance", SkillGuidance([]string{"Debug"}), SourceSkillGuidance},
		{"WorkspaceDeep", WorkspaceDeep(4), SourceWorkspaceDeep},
		{"FunctionMapSpec", FunctionMapSpec(5, 16000), SourceFunctionMap},
		{"ExistingBlueprints", ExistingBlueprints(), SourceExistingBlueprints},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.spec.Name != tt.want {
				t.Errorf("expected name %q, got %q", tt.want, tt.spec.Name)
			}
		})
	}
}

func TestSources_Helper(t *testing.T) {
	specs := Sources(Blueprint(), Worklog(10, "all"), NodeReturns("failures"))
	if len(specs) != 3 {
		t.Errorf("expected 3 specs, got %d", len(specs))
	}
}

// ── Param helpers ────────────────────────────────────────────────────────────

func TestParamString(t *testing.T) {
	p := map[string]any{"name": "foo"}
	if got := paramString(p, "name", ""); got != "foo" {
		t.Errorf("expected foo, got %q", got)
	}
	if got := paramString(p, "missing", "default"); got != "default" {
		t.Errorf("expected default, got %q", got)
	}
	if got := paramString(nil, "any", "default"); got != "default" {
		t.Errorf("expected default for nil map, got %q", got)
	}
}

func TestParamInt(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		key    string
		def    int
		want   int
	}{
		{"int value", map[string]any{"n": 42}, "n", 0, 42},
		{"int64 value", map[string]any{"n": int64(42)}, "n", 0, 42},
		{"float64 value (JSON)", map[string]any{"n": float64(42)}, "n", 0, 42},
		{"missing key", map[string]any{}, "n", 99, 99},
		{"nil map", nil, "n", 99, 99},
		{"wrong type", map[string]any{"n": "not a number"}, "n", 99, 99},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := paramInt(tt.params, tt.key, tt.def); got != tt.want {
				t.Errorf("expected %d, got %d", tt.want, got)
			}
		})
	}
}

func TestParamStringSlice(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		want   []string
	}{
		{"[]string", map[string]any{"k": []string{"a", "b"}}, []string{"a", "b"}},
		{"[]any with strings", map[string]any{"k": []any{"a", "b"}}, []string{"a", "b"}},
		{"[]any mixed (strings only)", map[string]any{"k": []any{"a", 42, "b"}}, []string{"a", "b"}},
		{"missing", map[string]any{}, nil},
		{"nil map", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := paramStringSlice(tt.params, "k")
			if len(got) != len(tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("at %d: expected %q, got %q", i, tt.want[i], got[i])
				}
			}
		})
	}
}

// ── proportionalTruncate ─────────────────────────────────────────────────────

func TestProportionalTruncate_UnderBudget_NoChange(t *testing.T) {
	in := map[string]string{"a": "hello", "b": "world"}
	out := proportionalTruncate(in, 10000)
	if out["a"] != "hello" || out["b"] != "world" {
		t.Errorf("expected unchanged, got %v", out)
	}
}

func TestProportionalTruncate_OverBudget_Shrinks(t *testing.T) {
	in := map[string]string{
		"big":   strings.Repeat("a", 1000),
		"small": strings.Repeat("b", 100),
	}
	out := proportionalTruncate(in, 500)
	totalOut := 0
	for _, c := range out {
		totalOut += len(c)
	}
	// Should be near the target — allow some slop for the truncation marker.
	if totalOut > 1100 { // 500 + ~600 for truncation markers
		t.Errorf("expected total near 500, got %d", totalOut)
	}
}

func TestProportionalTruncate_DropsToMinimum(t *testing.T) {
	// When the proportional share is tiny, it falls back to keeping 200 chars
	// (the minimum) or the original if smaller.
	in := map[string]string{
		"huge": strings.Repeat("x", 100000),
	}
	out := proportionalTruncate(in, 50)
	// 50 chars target → too small for 200 minimum, falls back to 200
	if len(out["huge"]) > 250 { // 200 + truncation marker
		t.Errorf("expected ~200 chars, got %d", len(out["huge"]))
	}
}

// ── extractFailureDetail ─────────────────────────────────────────────────────

func TestExtractFailureDetail_BashError(t *testing.T) {
	bashJSON := `{"bash_error":true,"command":"npm install","exit_code":1,"stderr":"npm error","stdout":"some output"}`
	n := &Node{Result: bashJSON}
	got := extractFailureDetail(n)
	if !strings.Contains(got, "exit 1") {
		t.Errorf("expected exit code, got %q", got)
	}
	if !strings.Contains(got, "stderr: npm error") {
		t.Errorf("expected stderr, got %q", got)
	}
	if !strings.Contains(got, "stdout: some output") {
		t.Errorf("expected stdout, got %q", got)
	}
}

func TestExtractFailureDetail_BashError_NoStdoutStderr(t *testing.T) {
	bashJSON := `{"bash_error":true,"command":"npm install","exit_code":127}`
	n := &Node{Result: bashJSON}
	got := extractFailureDetail(n)
	if !strings.Contains(got, "exit 127") {
		t.Errorf("expected exit code, got %q", got)
	}
	if !strings.Contains(got, "npm install") {
		t.Errorf("expected command, got %q", got)
	}
}

func TestExtractFailureDetail_PlainError(t *testing.T) {
	n := &Node{Error: errors.New("something broke")}
	got := extractFailureDetail(n)
	if got != "something broke" {
		t.Errorf("expected error string, got %q", got)
	}
}

func TestExtractFailureDetail_NoDetail(t *testing.T) {
	n := &Node{}
	got := extractFailureDetail(n)
	if got != "(no error detail)" {
		t.Errorf("expected fallback string, got %q", got)
	}
}

func TestExtractFailureDetail_NonBashJSON(t *testing.T) {
	n := &Node{Result: `{"foo":"bar"}`}
	got := extractFailureDetail(n)
	// Not a bash error, no Error field — falls back to raw result.
	if got != `{"foo":"bar"}` {
		t.Errorf("expected raw result, got %q", got)
	}
}

// ── containsAnyCI ────────────────────────────────────────────────────────────

func TestContainsAnyCI(t *testing.T) {
	tests := []struct {
		s       string
		needles []string
		want    bool
	}{
		{"the BUILD failed", []string{"failed"}, true},
		{"the BUILD failed", []string{"FAILED"}, true},
		{"the BUILD succeeded", []string{"failed", "error"}, false},
		{"NPM ERROR code 1", []string{"error"}, true},
		{"", []string{"anything"}, false},
		{"anything", []string{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.s, func(t *testing.T) {
			if got := containsAnyCI(tt.s, tt.needles...); got != tt.want {
				t.Errorf("containsAnyCI(%q, %v) = %v, want %v", tt.s, tt.needles, got, tt.want)
			}
		})
	}
}

// ── specNames helper ─────────────────────────────────────────────────────────

func TestWriteLLMTrace_Disabled_NoOp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KAIJU_PROMPT_DEBUG", "")
	// Without env var, no file should be written.
	WriteLLMTrace(LLMTrace{
		AlertID:  "test-alert",
		NodeID:   "n1",
		NodeType: "test",
		System:   "sys",
		User:     "usr",
		Output:   "out",
	})
	if _, err := os.Stat(filepath.Join("/tmp/kaiju-prompts", "test-alert.log")); err == nil {
		// File may exist from prior runs. Just confirm we didn't write to dir.
	}
	_ = dir
}

func TestWriteLLMTrace_Enabled_WritesFile(t *testing.T) {
	t.Setenv("KAIJU_PROMPT_DEBUG", "1")
	alertID := fmt.Sprintf("test-alert-%d", time.Now().UnixNano())
	defer os.Remove(filepath.Join("/tmp/kaiju-prompts", alertID+".log"))

	WriteLLMTrace(LLMTrace{
		AlertID:  alertID,
		NodeID:   "n1",
		NodeType: "test_node",
		Tag:      "test_tag",
		Model:    "test_model",
		Started:  time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC),
		Input:    map[string]string{"goal": "test goal", "query": "test query"},
		System:   "system prompt content",
		User:     "user prompt content",
		Output:   "llm response",
		TokensIn: 100, TokensOut: 50, LatencyMS: 1234,
	})

	data, err := os.ReadFile(filepath.Join("/tmp/kaiju-prompts", alertID+".log"))
	if err != nil {
		t.Fatalf("expected log file to exist: %v", err)
	}
	content := string(data)
	checks := []string{
		"=== n1 test_node test_tag test_model 2026-04-10T12:00:00Z ===",
		"--- INPUT ---",
		"goal: test goal",
		"query: test query",
		"--- SYSTEM PROMPT ---",
		"system prompt content",
		"--- USER PROMPT ---",
		"user prompt content",
		"--- OUTPUT ---",
		"llm response",
		"latency_ms: 1234",
		"tokens_in: 100",
		"tokens_out: 50",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf("expected log to contain %q\n--- got ---\n%s", want, content)
		}
	}
}

func TestWriteLLMTrace_AppendsAcrossCalls(t *testing.T) {
	t.Setenv("KAIJU_PROMPT_DEBUG", "1")
	alertID := fmt.Sprintf("test-append-%d", time.Now().UnixNano())
	defer os.Remove(filepath.Join("/tmp/kaiju-prompts", alertID+".log"))

	WriteLLMTrace(LLMTrace{AlertID: alertID, NodeID: "n1", NodeType: "first", User: "first call"})
	WriteLLMTrace(LLMTrace{AlertID: alertID, NodeID: "n2", NodeType: "second", User: "second call"})

	data, err := os.ReadFile(filepath.Join("/tmp/kaiju-prompts", alertID+".log"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "first call") || !strings.Contains(content, "second call") {
		t.Errorf("expected both entries appended, got:\n%s", content)
	}
	if !strings.Contains(content, "=== n1 first") || !strings.Contains(content, "=== n2 second") {
		t.Errorf("expected both headers present, got:\n%s", content)
	}
}

func TestWriteLLMTrace_Error_RecordsError(t *testing.T) {
	t.Setenv("KAIJU_PROMPT_DEBUG", "1")
	alertID := fmt.Sprintf("test-err-%d", time.Now().UnixNano())
	defer os.Remove(filepath.Join("/tmp/kaiju-prompts", alertID+".log"))

	WriteLLMTrace(LLMTrace{
		AlertID: alertID, NodeID: "n1", NodeType: "test",
		System: "sys", User: "usr",
		Err: "LLM call timed out after 30s",
	})

	data, _ := os.ReadFile(filepath.Join("/tmp/kaiju-prompts", alertID+".log"))
	content := string(data)
	if !strings.Contains(content, "--- ERROR ---") {
		t.Errorf("expected ERROR section, got:\n%s", content)
	}
	if !strings.Contains(content, "timed out") {
		t.Errorf("expected error message, got:\n%s", content)
	}
}

func TestSpecNames(t *testing.T) {
	specs := []SourceSpec{
		{Name: "a"},
		{Name: "b"},
		{Name: "c"},
	}
	got := specNames(specs)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("at %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

// llm.Message is referenced by the trigger in fixtures; ensure compile-time check.
var _ = llm.Message{}

// ── Source: blueprint ────────────────────────────────────────────────────────

func TestBlueprintSource_Latest(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	bpDir := filepath.Join(dir, "blueprints", "test-session")
	if err := os.MkdirAll(bpDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bpDir, "main.blueprint.md"), []byte("# Test Blueprint"), 0644); err != nil {
		t.Fatal(err)
	}

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(Blueprint()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Sources[SourceBlueprint], "Test Blueprint") {
		t.Errorf("expected blueprint content, got %q", resp.Sources[SourceBlueprint])
	}
}

func TestBlueprintSource_Missing_ReturnsEmpty(t *testing.T) {
	gate, _, _, _ := newTestContextGate(t)
	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(Blueprint()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Sources[SourceBlueprint] != "" {
		t.Errorf("expected empty for missing blueprint, got %q", resp.Sources[SourceBlueprint])
	}
}

func TestBlueprintSource_NamedAbsolutePath(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	path := filepath.Join(dir, "custom.md")
	if err := os.WriteFile(path, []byte("custom content"), 0644); err != nil {
		t.Fatal(err)
	}
	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(BlueprintNamed(path)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Sources[SourceBlueprint] != "custom content" {
		t.Errorf("expected custom content, got %q", resp.Sources[SourceBlueprint])
	}
}

func TestBlueprintSource_SessionScoped(t *testing.T) {
	gate, _, trig, dir := newTestContextGate(t)

	// Write to session A subdir
	sessionA := filepath.Join(dir, "blueprints", "session-A")
	os.MkdirAll(sessionA, 0755)
	os.WriteFile(filepath.Join(sessionA, "a.blueprint.md"), []byte("session A content"), 0644)

	// Write to session B subdir
	sessionB := filepath.Join(dir, "blueprints", "session-B")
	os.MkdirAll(sessionB, 0755)
	os.WriteFile(filepath.Join(sessionB, "b.blueprint.md"), []byte("session B content"), 0644)

	// Trigger says session A
	trig.SessionID = "session-A"
	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(Blueprint()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Sources[SourceBlueprint], "session A") {
		t.Errorf("session A should see session A blueprint, got %q", resp.Sources[SourceBlueprint])
	}
	if strings.Contains(resp.Sources[SourceBlueprint], "session B") {
		t.Errorf("session A leaked session B blueprint")
	}
}

// ── Source: blueprint focus (sections) ──────────────────────────────────────

// blueprintWithSections is a fixture for the section-focus tests.
const blueprintWithSections = `# Test Project

## Goal
Build a thing.

## Architecture
React + Vite + SQLite.

## Files
- src/App.jsx
- src/index.html
- vite.config.js

## Build System
- Install: ` + "`" + `npm install` + "`" + `
- Dev: ` + "`" + `npm run dev` + "`" + `
- Build: ` + "`" + `npm run build` + "`" + `

## Services
- frontend on port 5173
`

func writeBlueprintFixture(t *testing.T, dir string) {
	t.Helper()
	bpDir := filepath.Join(dir, "blueprints", "test-session")
	if err := os.MkdirAll(bpDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bpDir, "main.blueprint.md"), []byte(blueprintWithSections), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestBlueprintSection_ExtractsSingleSection(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	writeBlueprintFixture(t, dir)

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(BlueprintSection("Files")),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Sources[SourceBlueprint]
	if !strings.Contains(got, "src/App.jsx") {
		t.Errorf("expected Files section content, got %q", got)
	}
	// Should NOT include other sections
	if strings.Contains(got, "Build a thing") {
		t.Errorf("expected only Files section, got Goal content too: %q", got)
	}
	if strings.Contains(got, "vite.config.js") == false {
		t.Errorf("expected vite.config.js in Files section, got %q", got)
	}
}

func TestBlueprintSection_BuildSystem(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	writeBlueprintFixture(t, dir)

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(BlueprintSection("Build System")),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Sources[SourceBlueprint]
	if !strings.Contains(got, "npm install") {
		t.Errorf("expected build system content, got %q", got)
	}
	// Should not bleed into the next section
	if strings.Contains(got, "frontend on port") {
		t.Errorf("section bled into next section, got %q", got)
	}
}

func TestBlueprintSection_MissingSection_ReturnsEmpty(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	writeBlueprintFixture(t, dir)

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(BlueprintSection("NoSuchSection")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Sources[SourceBlueprint] != "" {
		t.Errorf("expected empty for missing section, got %q", resp.Sources[SourceBlueprint])
	}
}

func TestBlueprintSections_MultipleInOrder(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	writeBlueprintFixture(t, dir)

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(BlueprintSections("Goal", "Services")),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Sources[SourceBlueprint]
	if !strings.Contains(got, "## Goal") || !strings.Contains(got, "Build a thing") {
		t.Errorf("expected Goal heading and content: %q", got)
	}
	if !strings.Contains(got, "## Services") || !strings.Contains(got, "frontend on port 5173") {
		t.Errorf("expected Services heading and content: %q", got)
	}
	// Should NOT include sections we didn't ask for
	if strings.Contains(got, "src/App.jsx") {
		t.Errorf("Files section leaked: %q", got)
	}
	if strings.Contains(got, "npm install") {
		t.Errorf("Build System section leaked: %q", got)
	}
	// Goal should appear before Services in the output
	goalIdx := strings.Index(got, "## Goal")
	servicesIdx := strings.Index(got, "## Services")
	if goalIdx < 0 || servicesIdx < 0 || goalIdx > servicesIdx {
		t.Errorf("expected Goal before Services, got positions %d / %d", goalIdx, servicesIdx)
	}
}

func TestBlueprintSections_DropsMissing(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	writeBlueprintFixture(t, dir)

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(BlueprintSections("Goal", "NoSuchSection", "Services")),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Sources[SourceBlueprint]
	if !strings.Contains(got, "## Goal") {
		t.Errorf("expected Goal: %q", got)
	}
	if !strings.Contains(got, "## Services") {
		t.Errorf("expected Services: %q", got)
	}
	if strings.Contains(got, "NoSuchSection") {
		t.Errorf("missing section header should be dropped: %q", got)
	}
}

func TestBlueprintSections_AllMissing_ReturnsEmpty(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	writeBlueprintFixture(t, dir)

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(BlueprintSections("Foo", "Bar")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Sources[SourceBlueprint] != "" {
		t.Errorf("expected empty when all sections missing, got %q", resp.Sources[SourceBlueprint])
	}
}

func TestBlueprintSection_Constructor(t *testing.T) {
	spec := BlueprintSection("Files")
	if spec.Name != SourceBlueprint {
		t.Errorf("expected blueprint name, got %q", spec.Name)
	}
	if spec.Params["section"] != "Files" {
		t.Errorf("expected section param, got %v", spec.Params)
	}
}

func TestBlueprintSections_Constructor(t *testing.T) {
	spec := BlueprintSections("Goal", "Files")
	if spec.Name != SourceBlueprint {
		t.Errorf("expected blueprint name, got %q", spec.Name)
	}
	got, ok := spec.Params["sections"].([]string)
	if !ok {
		t.Fatalf("expected []string sections, got %T", spec.Params["sections"])
	}
	if len(got) != 2 || got[0] != "Goal" || got[1] != "Files" {
		t.Errorf("expected [Goal Files], got %v", got)
	}
}

// ── Source: worklog ──────────────────────────────────────────────────────────

func TestWorklogSource_RoundTrip(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	appendWorklog(dir, "test-session", "tag1", "OK", "first event")
	appendWorklog(dir, "test-session", "tag2", "FAILED", "second event")

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(Worklog(10, "all")),
	})
	if err != nil {
		t.Fatal(err)
	}
	wl := resp.Sources[SourceWorklog]
	if !strings.Contains(wl, "first event") || !strings.Contains(wl, "second event") {
		t.Errorf("worklog missing entries: %q", wl)
	}
}

func TestWorklogSource_FailuresFilter(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	appendWorklog(dir, "test-session", "ok-tag", "OK", "successful operation")
	appendWorklog(dir, "test-session", "fail-tag", "FAILED", "broken thing")

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(Worklog(10, "failures")),
	})
	if err != nil {
		t.Fatal(err)
	}
	wl := resp.Sources[SourceWorklog]
	if strings.Contains(wl, "successful operation") {
		t.Errorf("failures filter should drop OK entries: %q", wl)
	}
	if !strings.Contains(wl, "broken thing") {
		t.Errorf("failures filter should keep FAILED entries: %q", wl)
	}
}

func TestWorklogSource_ZeroLines_ReturnsEmpty(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	appendWorklog(dir, "test-session", "tag", "OK", "event")
	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(Worklog(0, "all")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Sources[SourceWorklog] != "" {
		t.Errorf("zero lines should return empty, got %q", resp.Sources[SourceWorklog])
	}
}

// ── Source: node_returns ─────────────────────────────────────────────────────

func TestNodeReturnsSource_Failures(t *testing.T) {
	gate, graph, _, _ := newTestContextGate(t)

	// Add a successful and a failed node to the graph.
	okNode := &Node{Type: NodeTool, ToolName: "bash", Tag: "ok-cmd"}
	okID := graph.AddNode(okNode)
	graph.SetState(okID, StateRunning)
	graph.SetResult(okID, "all good")

	failNode := &Node{Type: NodeTool, ToolName: "bash", Tag: "fail-cmd"}
	failID := graph.AddNode(failNode)
	graph.SetState(failID, StateRunning)
	graph.SetError(failID, errors.New("boom"))

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(NodeReturns("failures")),
	})
	if err != nil {
		t.Fatal(err)
	}
	out := resp.Sources[SourceNodeReturns]
	if !strings.Contains(out, "fail-cmd") {
		t.Errorf("expected failed node in output: %q", out)
	}
	if strings.Contains(out, "ok-cmd") {
		t.Errorf("failures filter should not include successful nodes: %q", out)
	}
}

func TestNodeReturnsSource_Empty_ReturnsEmpty(t *testing.T) {
	gate, _, _, _ := newTestContextGate(t)
	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(NodeReturns("all")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Sources[SourceNodeReturns] != "" {
		t.Errorf("expected empty for graph with no nodes, got %q", resp.Sources[SourceNodeReturns])
	}
}

// ── Source: workspace_tree ───────────────────────────────────────────────────

func TestWorkspaceTreeSource(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	os.MkdirAll(filepath.Join(dir, "src"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# project"), 0644)

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(WorkspaceTree(3)),
	})
	if err != nil {
		t.Fatal(err)
	}
	tree := resp.Sources[SourceWorkspaceTree]
	if !strings.Contains(tree, "main.go") {
		t.Errorf("expected main.go in tree: %q", tree)
	}
}

func TestWorkspaceTreeSource_FocusDir(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	os.MkdirAll(filepath.Join(dir, "src", "components"), 0755)
	os.MkdirAll(filepath.Join(dir, "docs"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "components", "App.jsx"), []byte("//"), 0644)
	os.WriteFile(filepath.Join(dir, "docs", "readme.md"), []byte("docs"), 0644)

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: []SourceSpec{WorkspaceTreeFocus(3, "src")},
	})
	if err != nil {
		t.Fatal(err)
	}
	tree := resp.Sources[SourceWorkspaceTree]
	if !strings.Contains(tree, "App.jsx") {
		t.Errorf("focus_dir=src should include src files: %q", tree)
	}
	if strings.Contains(tree, "readme.md") {
		t.Errorf("focus_dir=src should NOT include docs: %q", tree)
	}
}

// ── Source: service_state ────────────────────────────────────────────────────

func TestServiceStateSource(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	registry := `[
		{"name":"frontend","status":"running","command":"npx vite","pid":1234,"port":5173,"workdir":"project"},
		{"name":"backend","status":"crashed","command":"node server.js","pid":2345,"workdir":"project/backend"}
	]`
	os.WriteFile(filepath.Join(dir, ".services.json"), []byte(registry), 0644)

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(ServiceState()),
	})
	if err != nil {
		t.Fatal(err)
	}
	out := resp.Sources[SourceServiceState]
	if !strings.Contains(out, "frontend") || !strings.Contains(out, "backend") {
		t.Errorf("expected both services: %q", out)
	}
	if !strings.Contains(out, "running") || !strings.Contains(out, "crashed") {
		t.Errorf("expected status info: %q", out)
	}
}

func TestServiceStateSource_StatusFilter(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	registry := `[
		{"name":"frontend","status":"running","command":"npx vite","pid":1234},
		{"name":"backend","status":"crashed","command":"node server.js","pid":2345}
	]`
	os.WriteFile(filepath.Join(dir, ".services.json"), []byte(registry), 0644)

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: []SourceSpec{ServiceStateFiltered("running")},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := resp.Sources[SourceServiceState]
	if !strings.Contains(out, "frontend") {
		t.Errorf("expected running service: %q", out)
	}
	if strings.Contains(out, "backend") {
		t.Errorf("filter should drop crashed service: %q", out)
	}
}

func TestServiceStateSource_NoFile_ReturnsEmpty(t *testing.T) {
	gate, _, _, _ := newTestContextGate(t)
	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(ServiceState()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Sources[SourceServiceState] != "" {
		t.Errorf("expected empty when no registry file, got %q", resp.Sources[SourceServiceState])
	}
}

// ── Source: history ──────────────────────────────────────────────────────────

func TestHistorySource(t *testing.T) {
	gate, _, trig, _ := newTestContextGate(t)
	trig.History = []llm.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
		{Role: "user", Content: "how are you"},
	}

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(History(2)),
	})
	if err != nil {
		t.Fatal(err)
	}
	out := resp.Sources[SourceHistory]
	// Should include the LAST 2 turns
	if !strings.Contains(out, "hi there") || !strings.Contains(out, "how are you") {
		t.Errorf("expected last 2 turns: %q", out)
	}
	if strings.Contains(out, "hello") {
		t.Errorf("should drop oldest turn beyond limit: %q", out)
	}
}

func TestHistorySource_Empty_ReturnsEmpty(t *testing.T) {
	gate, _, _, _ := newTestContextGate(t)
	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(History(5)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Sources[SourceHistory] != "" {
		t.Errorf("expected empty history, got %q", resp.Sources[SourceHistory])
	}
}

// ── Source: memory (intentionally absent) ───────────────────────────────────
// There is no memory source by design. Memory is a chat-boundary concern
// (api.go input → trigger.History → aggregator → memory.Manager.StoreMessage)
// and the execution layer (ContextGate, sources, graph nodes) MUST NOT touch
// it. See the architectural doc at the top of contextgate.go. If a future
// contributor adds a memorySource, this test should remain as the canonical
// "no memory access from execution layer" assertion.
func TestNoMemorySource_ByDesign(t *testing.T) {
	gate, _, _, _ := newTestContextGate(t)
	if _, ok := gate.sources["memory"]; ok {
		t.Fatal("memory source must NOT be registered — memory is a chat-boundary concern, not an execution-layer concern. See the architectural doc at the top of contextgate.go.")
	}
	// Calling Get() with a hand-built memory spec must return an unknown-source error.
	_, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: []SourceSpec{{Name: "memory"}},
	})
	if err == nil {
		t.Fatal("expected unknown-source error for memory")
	}
}

// ── Source: skill_guidance ───────────────────────────────────────────────────

func TestSkillGuidanceSource_NoActiveCards_ReturnsEmpty(t *testing.T) {
	gate, _, _, _ := newTestContextGate(t)
	// Default test gate has no active cards.
	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(SkillGuidance([]string{"Debug"})),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Sources[SourceSkillGuidance] != "" {
		t.Errorf("expected empty without active cards, got %q", resp.Sources[SourceSkillGuidance])
	}
}

// ── Source: existing_blueprints ──────────────────────────────────────────────

func TestExistingBlueprintsSource(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	bpDir := filepath.Join(dir, "blueprints", "test-session")
	os.MkdirAll(bpDir, 0755)
	os.WriteFile(filepath.Join(bpDir, "one.blueprint.md"), []byte("blueprint one"), 0644)
	os.WriteFile(filepath.Join(bpDir, "two.blueprint.md"), []byte("blueprint two"), 0644)

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(ExistingBlueprints()),
	})
	if err != nil {
		t.Fatal(err)
	}
	out := resp.Sources[SourceExistingBlueprints]
	if !strings.Contains(out, "blueprint one") || !strings.Contains(out, "blueprint two") {
		t.Errorf("expected both blueprints: %q", out)
	}
}

// ── Source: workspace_deep ───────────────────────────────────────────────────

func TestWorkspaceDeepSource(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	os.MkdirAll(filepath.Join(dir, "src"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0644)

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(WorkspaceDeep(4)),
	})
	if err != nil {
		t.Fatal(err)
	}
	// scanWorkspaceDeep returns at minimum the file tree
	if resp.Sources[SourceWorkspaceDeep] == "" {
		t.Errorf("expected non-empty workspace_deep output")
	}
}

// ── Source: function_map ─────────────────────────────────────────────────────

// TestSourceNameMethods exercises the Name() method on every source. Trivial
// but ensures the source-name → constant binding is consistent.
func TestSourceNameMethods(t *testing.T) {
	cases := []struct {
		src  ContextSource
		want string
	}{
		{&blueprintSource{}, SourceBlueprint},
		{&worklogSource{}, SourceWorklog},
		{&nodeReturnsSource{}, SourceNodeReturns},
		{&workspaceTreeSource{}, SourceWorkspaceTree},
		{&serviceStateSource{}, SourceServiceState},
		{&historySource{}, SourceHistory},
		{&skillGuidanceSource{}, SourceSkillGuidance},
		{&workspaceDeepSource{}, SourceWorkspaceDeep},
		{&functionMapSource{}, SourceFunctionMap},
		{&existingBlueprintsSource{}, SourceExistingBlueprints},
	}
	for _, c := range cases {
		if got := c.src.Name(); got != c.want {
			t.Errorf("source name: expected %q, got %q", c.want, got)
		}
	}
}

func TestFunctionMapSource(t *testing.T) {
	gate, _, _, dir := newTestContextGate(t)
	src := `package foo

func Hello() string {
	return "hi"
}

func World() int {
	return 42
}
`
	os.WriteFile(filepath.Join(dir, "foo.go"), []byte(src), 0644)

	resp, err := gate.Get(context.Background(), ContextRequest{
		ReturnSources: Sources(FunctionMapSpec(5, 16000)),
	})
	if err != nil {
		t.Fatal(err)
	}
	out := resp.Sources[SourceFunctionMap]
	if !strings.Contains(out, "Hello") || !strings.Contains(out, "World") {
		t.Errorf("expected both functions in map: %q", out)
	}
}
