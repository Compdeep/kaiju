package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeTag(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello world", "hello_world"},
		{"scaffold Vue 3 + Vite project with Tailwind CSS", "scaffold_Vue_3___Vite_project_with_Tailw"},
		{"simple", "simple"},
		{"a/b/c.py", "a_b_c_py"},
		{"", ""},
		{"___leading_trailing___", "leading_trailing"},
	}
	for _, tt := range tests {
		got := sanitizeTag(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeTag(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestComputePlanOutputWithSetup(t *testing.T) {
	raw := `{
		"plan": "# Architecture",
		"setup": ["mkdir -p project/src", "npm install express"],
		"tasks": [
			{"goal": "create backend", "task_files": ["src/server.js"], "brief": "Express app", "execute": "node src/server.js", "depends_on_tasks": []}
		]
	}`
	var out computePlanOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(out.Setup) != 2 {
		t.Errorf("expected 2 setup commands, got %d", len(out.Setup))
	}
	if out.Setup[0] != "mkdir -p project/src" {
		t.Errorf("setup[0] = %q", out.Setup[0])
	}
	if out.Tasks[0].Execute != "node src/server.js" {
		t.Errorf("task execute = %q", out.Tasks[0].Execute)
	}
}

func TestBuildComputeUserPrompt(t *testing.T) {
	prompt := buildComputeUserPrompt("compute pi", "what is pi?", nil, nil, "")
	if prompt == "" {
		t.Error("buildComputeUserPrompt returned empty")
	}
	if !contains(prompt, "## Goal") {
		t.Error("missing Goal section")
	}
	if !contains(prompt, "compute pi") {
		t.Error("missing goal text")
	}
	if !contains(prompt, "what is pi?") {
		t.Error("missing query text")
	}

	// With hints
	prompt = buildComputeUserPrompt("compute pi", "", nil, []any{"NameError: math not defined"}, "")
	if !contains(prompt, "Previous Attempts") {
		t.Error("missing hints section")
	}
	if !contains(prompt, "NameError") {
		t.Error("missing hint text")
	}

	// With plan
	prompt = buildComputeUserPrompt("compute pi", "", nil, nil, "Use math.pi from stdlib")
	if !contains(prompt, "Blueprint") {
		t.Error("missing plan section")
	}
}

func TestComputePlanOutputParsing(t *testing.T) {
	// Valid structured output
	raw := `{
		"plan": "# Architecture\n\nUse React + Express",
		"tasks": [
			{"goal": "scaffold frontend", "depends_on_tasks": []},
			{"goal": "create backend API", "depends_on_tasks": []},
			{"goal": "wire frontend to backend", "depends_on_tasks": [0, 1]}
		]
	}`

	var out computePlanOutput
	err := json.Unmarshal([]byte(raw), &out)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(out.Tasks) != 3 {
		t.Errorf("expected 3 work items, got %d", len(out.Tasks))
	}
	if out.Tasks[0].Goal != "scaffold frontend" {
		t.Errorf("item 0 goal = %q", out.Tasks[0].Goal)
	}
	if len(out.Tasks[2].DependsOnTasks) != 2 {
		t.Errorf("item 2 deps = %v, want [0,1]", out.Tasks[2].DependsOnTasks)
	}
}

func TestRewriteDependentsMultiExcluding(t *testing.T) {
	g := NewGraph()

	// Parent node (compute plan)
	parent := &Node{Type: NodeCompute, Tag: "plan"}
	parentID := g.AddNode(parent)

	// Two child nodes (work items)
	child1 := &Node{Type: NodeCompute, Tag: "child1", DependsOn: []string{parentID}}
	child2 := &Node{Type: NodeCompute, Tag: "child2", DependsOn: []string{parentID}}
	child1ID := g.AddNode(child1)
	child2ID := g.AddNode(child2)

	// Downstream node that depended on parent
	downstream := &Node{Type: NodeTool, Tag: "downstream", DependsOn: []string{parentID}}
	downstreamID := g.AddNode(downstream)

	// Rewrite: downstream should now depend on both children, not parent
	rewriteDependentsMultiExcluding(g, parentID, []*Node{child1, child2})

	// Check downstream now depends on both children
	dsNode := g.Get(downstreamID)
	if len(dsNode.DependsOn) != 2 {
		t.Fatalf("downstream deps = %v, want 2 entries", dsNode.DependsOn)
	}
	if dsNode.DependsOn[0] != child1ID || dsNode.DependsOn[1] != child2ID {
		t.Errorf("downstream deps = %v, want [%s, %s]", dsNode.DependsOn, child1ID, child2ID)
	}

	// Check children still depend on parent (not rewritten)
	c1 := g.Get(child1ID)
	if len(c1.DependsOn) != 1 || c1.DependsOn[0] != parentID {
		t.Errorf("child1 deps = %v, want [%s]", c1.DependsOn, parentID)
	}
	c2 := g.Get(child2ID)
	if len(c2.DependsOn) != 1 || c2.DependsOn[0] != parentID {
		t.Errorf("child2 deps = %v, want [%s]", c2.DependsOn, parentID)
	}
}

func TestPlanStepsToNodesComputeType(t *testing.T) {
	g := NewGraph()
	b := NewBudget(100, 10, 50, 50, 0)

	steps := []PlanStep{
		{Tool: "web_search", Params: map[string]any{"query": "test"}, Tag: "search"},
		{Type: "compute", Tool: "compute", Params: map[string]any{"goal": "analyze"}, Tag: "analyze"},
		{Tool: "compute", Params: map[string]any{"goal": "fallback"}, Tag: "fallback"}, // no type field, but tool=compute
	}

	nodes, err := planStepsToNodes(steps, g, b, nil)
	if err != nil {
		t.Fatalf("planStepsToNodes error: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(nodes))
	}

	if nodes[0].Type != NodeTool {
		t.Errorf("node 0 type = %v, want NodeTool", nodes[0].Type)
	}
	if nodes[1].Type != NodeCompute {
		t.Errorf("node 1 type = %v, want NodeCompute (type field)", nodes[1].Type)
	}
	if nodes[2].Type != NodeCompute {
		t.Errorf("node 2 type = %v, want NodeCompute (tool name fallback)", nodes[2].Type)
	}
}

func TestComputeWorkItemParsingWithOwnership(t *testing.T) {
	raw := `{
		"plan": "# Architecture\n\nUse React + Express",
		"tasks": [
			{
				"goal": "scaffold frontend",
				"task_files": ["src/App.vue", "src/main.js", "package.json"],
				"brief": "Use Vue 3 with Vite. Tailwind for styling.",
				"depends_on_tasks": []
			},
			{
				"goal": "create backend API",
				"task_files": ["backend/server.js", "backend/routes/auth.js"],
				"brief": "Express 4. JWT auth. Connect to Postgres.",
				"depends_on_tasks": [0]
			}
		]
	}`

	var out computePlanOutput
	err := json.Unmarshal([]byte(raw), &out)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(out.Tasks) != 2 {
		t.Fatalf("expected 2 work items, got %d", len(out.Tasks))
	}

	// Item 0
	if len(out.Tasks[0].TaskFiles) != 3 {
		t.Errorf("item 0 task_files = %v, want 3", out.Tasks[0].TaskFiles)
	}
	if out.Tasks[0].Brief == "" {
		t.Error("item 0 brief is empty")
	}

	// Item 1
	if len(out.Tasks[1].DependsOnTasks) != 1 || out.Tasks[1].DependsOnTasks[0] != 0 {
		t.Errorf("item 1 depends_on_tasks = %v, want [0]", out.Tasks[1].DependsOnTasks)
	}
}

func TestScanWorkspaceDeep(t *testing.T) {
	// Create temp workspace
	dir := t.TempDir()

	// Small file (< 3KB) — should include full content
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"port": 8080}`), 0644)

	// Larger file (> 3KB) — should extract signatures
	bigContent := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n\nfunc helper() string {\n\treturn \"hi\"\n}\n"
	bigContent += strings.Repeat("// padding\n", 300)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(bigContent), 0644)

	// Binary file — should be skipped
	os.WriteFile(filepath.Join(dir, "image.png"), []byte("fake png"), 0644)

	// Nested dir
	os.MkdirAll(filepath.Join(dir, "src"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "app.py"), []byte("def hello():\n    pass\n"), 0644)

	result := scanWorkspaceDeep(dir, 3)

	if !contains(result, "config.json") {
		t.Error("missing config.json")
	}
	if !contains(result, `"port": 8080`) {
		t.Error("small file content not included")
	}
	if !contains(result, "main.go") {
		t.Error("missing main.go")
	}
	if !contains(result, "func main()") {
		t.Error("missing Go function signature")
	}
	if contains(result, "image.png") {
		t.Error("binary file should be skipped")
	}
	if !contains(result, "app.py") {
		t.Error("missing nested app.py")
	}
}

func TestBuildComputeUserPromptWithOwnership(t *testing.T) {
	prompt := buildComputeUserPrompt("create backend", "build webapp", nil, nil, "Use Express 4")
	if !contains(prompt, "create backend") {
		t.Error("missing goal")
	}
	if !contains(prompt, "Blueprint") {
		t.Error("missing plan section")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ── extractBlueprint tests ──

func TestExtractBlueprint_ValidJSON(t *testing.T) {
	raw := `{
  "blueprint": "# My Project\n\n## Goal\nBuild something.\n\n## Architecture\nUse Go.\n",
  "interfaces": {"GET /health": {"response": "ok"}},
  "tasks": []
}`
	got := extractBlueprint(raw)
	if !strings.Contains(got, "# My Project") {
		t.Errorf("expected markdown, got: %q", got)
	}
	if !strings.Contains(got, "## Architecture") {
		t.Error("missing Architecture section")
	}
	if strings.Contains(got, `"interfaces"`) {
		t.Error("leaked JSON outside the blueprint string")
	}
}

func TestExtractBlueprint_OldPlanKey(t *testing.T) {
	raw := `{"plan": "# Old Plan\nStuff here\n", "tasks": []}`
	got := extractBlueprint(raw)
	if !strings.Contains(got, "# Old Plan") {
		t.Errorf("expected old plan key to work, got: %q", got)
	}
}

func TestExtractBlueprint_EscapedContent(t *testing.T) {
	raw := `{"blueprint": "# Title\n\nSome \"quoted\" text.\nLine two.\n"}`
	got := extractBlueprint(raw)
	if !strings.Contains(got, `"quoted"`) {
		t.Errorf("escaped quotes not unescaped: %q", got)
	}
	if !strings.Contains(got, "Line two.") {
		t.Error("newlines not parsed")
	}
}

func TestExtractBlueprint_RealWorldFixture(t *testing.T) {
	data, err := os.ReadFile("/tmp/test_blueprint_raw.json")
	if err != nil {
		t.Skip("test fixture /tmp/test_blueprint_raw.json not available")
	}
	got := extractBlueprint(string(data))
	if got == "" {
		t.Fatal("extraction returned empty from real fixture")
	}
	if !strings.Contains(got, "Kaiju") {
		t.Error("missing Kaiju heading")
	}
	if !strings.Contains(got, "## Architecture") {
		t.Error("missing Architecture section")
	}
	if !strings.Contains(got, "## Directory Structure") {
		t.Error("missing Directory Structure section")
	}
	if !strings.Contains(got, "better-sqlite3") {
		t.Error("missing better-sqlite3 reference")
	}
	if strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Error("extracted content starts with { — got JSON not markdown")
	}
}

func TestExtractBlueprint_NoKey(t *testing.T) {
	got := extractBlueprint(`{"interfaces": {}, "tasks": []}`)
	if got != "" {
		t.Errorf("expected empty for no blueprint key, got: %q", got)
	}
}

func TestExtractBlueprint_Garbage(t *testing.T) {
	got := extractBlueprint("this is not json at all")
	if got != "" {
		t.Errorf("expected empty for garbage, got: %q", got)
	}
}
