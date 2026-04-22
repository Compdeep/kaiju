// Package editor is the pre-integration editor-layer eval. It drives real
// LLM calls against a fixture corpus to test the coder-edit pipeline at
// the editor layer in isolation (one LLM call per scenario).
//
// Not part of `go test ./...`. Invoked via the unified dispatcher at
// tests/eval/cmd.
package editor

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/internal/agent"
	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/config"
)

// scenario is one edit test. Every fixture has a companion
// <fixture>.scenarios.jsonl file, one scenario per line.
type scenario struct {
	Query       string `json:"query"`         // free-form instruction to the coder
	Check       string `json:"check"`         // shell command; $FILE expands to the edited copy
	ExpectIn    string `json:"expect_in"`     // optional: substring that MUST appear in the result
	ExpectNotIn string `json:"expect_not_in"` // optional: substring that must NOT appear
	Skip        string `json:"skip"`          // if non-empty, skip with this reason
	Core        bool   `json:"core"`          // true = part of the core tier (cheap smoke gate); false = extended
}

type result struct {
	Fixture    string
	Query      string
	Status     string // pass | skip | llm_fail | parse_fail | edit_fail | check_fail | assert_fail
	Reason     string
	DurationMs int64
}

// Options configure a Run of the editor eval. Fields left zero take sensible defaults.
type Options struct {
	ConfigPath string        // path to kaiju.json; default "kaiju.json"
	CorpusDir  string        // fixture root; default "tests/eval/editor/corpus"
	ReportOut  string        // markdown report path; default "tests/eval/editor/report.md"
	Only       string        // run only fixtures whose path contains this substring
	Dry        bool          // parse scenarios and print count only, no LLM calls
	Tier       string        // "core" | "extended" | "all" (default "all"); only "core"-tagged scenarios run when tier="core"
	Timeout    time.Duration // total run timeout
}

// Run executes the editor eval with the given options and returns the number
// of failed scenarios (0 = all passed). Errors are logged and returned as
// non-zero exit intent.
func Run(opts Options) int {
	if opts.ConfigPath == "" {
		opts.ConfigPath = "kaiju.json"
	}
	if opts.CorpusDir == "" {
		opts.CorpusDir = "tests/eval/editor/corpus"
	}
	if opts.ReportOut == "" {
		opts.ReportOut = "tests/eval/editor/report.md"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Minute
	}

	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		log.Printf("[editor] load config %s: %v", opts.ConfigPath, err)
		return 1
	}
	if cfg.LLM.APIKey == "" {
		log.Printf("[editor] no LLM API key in %s (or unresolved ${ENV})", opts.ConfigPath)
		return 1
	}

	pairs, err := discoverScenarios(opts.CorpusDir, opts.Only)
	if err != nil {
		log.Printf("[editor] discover scenarios: %v", err)
		return 1
	}
	total := 0
	for _, p := range pairs {
		total += len(p.scenarios)
	}
	log.Printf("[editor] discovered %d scenarios across %d fixtures (tier=%s)", total, len(pairs), opts.Tier)

	if opts.Dry {
		for _, p := range pairs {
			log.Printf("  %s → %d scenario(s)", p.fixturePath, len(p.scenarios))
		}
		return 0
	}

	client := llm.NewClientWithProvider(cfg.LLM.Provider, cfg.LLM.Endpoint, cfg.LLM.APIKey, cfg.LLM.Model)

	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()

	var results []result
	for _, p := range pairs {
		for _, sc := range p.scenarios {
			// Tier filter: when tier="core", only scenarios tagged Core run.
			// When tier="extended" or "all", everything runs.
			if opts.Tier == "core" && !sc.Core {
				continue
			}
			r := runScenario(ctx, client, p.fixturePath, sc)
			results = append(results, r)
			summaryLine(r)
		}
	}

	if err := writeReport(opts.ReportOut, results); err != nil {
		log.Printf("[editor] report write: %v", err)
	}
	log.Printf("[editor] summary: %s", tally(results))
	log.Printf("[editor] report: %s", opts.ReportOut)

	failed := 0
	for _, r := range results {
		if r.Status != "pass" && r.Status != "skip" {
			failed++
		}
	}
	return failed
}

// ── Scenario discovery ──────────────────────────────────────────────────────

type pair struct {
	fixturePath string // absolute path to the fixture file
	scenarios   []scenario
}

func discoverScenarios(root, only string) ([]pair, error) {
	var pairs []pair
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".scenarios.jsonl") {
			return nil
		}
		fixture := strings.TrimSuffix(path, ".scenarios.jsonl")
		if _, ferr := os.Stat(fixture); ferr != nil {
			return fmt.Errorf("%s: fixture %s missing: %w", path, fixture, ferr)
		}
		if only != "" && !strings.Contains(fixture, only) {
			return nil
		}
		scenarios, perr := parseScenarioFile(path)
		if perr != nil {
			return fmt.Errorf("%s: %w", path, perr)
		}
		pairs = append(pairs, pair{fixturePath: fixture, scenarios: scenarios})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].fixturePath < pairs[j].fixturePath })
	return pairs, nil
}

func parseScenarioFile(path string) ([]scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []scenario
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		var sc scenario
		if err := json.Unmarshal([]byte(line), &sc); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		if sc.Query == "" {
			return nil, fmt.Errorf("line %d: missing query", i+1)
		}
		out = append(out, sc)
	}
	return out, nil
}

// ── One scenario ────────────────────────────────────────────────────────────

func runScenario(ctx context.Context, client *llm.Client, fixturePath string, sc scenario) result {
	start := time.Now()
	r := result{Fixture: relPath(fixturePath), Query: sc.Query}

	if sc.Skip != "" {
		r.Status = "skip"
		r.Reason = sc.Skip
		r.DurationMs = time.Since(start).Milliseconds()
		return r
	}

	original, err := os.ReadFile(fixturePath)
	if err != nil {
		r.Status = "edit_fail"
		r.Reason = fmt.Sprintf("read fixture: %v", err)
		r.DurationMs = time.Since(start).Milliseconds()
		return r
	}

	tmpDir, err := os.MkdirTemp("", "editor-eval-*")
	if err != nil {
		r.Status = "edit_fail"
		r.Reason = fmt.Sprintf("mkdirtemp: %v", err)
		r.DurationMs = time.Since(start).Milliseconds()
		return r
	}
	defer os.RemoveAll(tmpDir)

	baseName := filepath.Base(fixturePath)
	workFile := filepath.Join(tmpDir, baseName)
	if err := os.WriteFile(workFile, original, 0644); err != nil {
		r.Status = "edit_fail"
		r.Reason = fmt.Sprintf("write temp copy: %v", err)
		r.DurationMs = time.Since(start).Milliseconds()
		return r
	}

	// Call the coder with the real system prompt + tool def.
	systemPrompt, toolDef := agent.EditorEvalBundle()
	userPrompt := buildUserPrompt(baseName, string(original), sc.Query)

	resp, err := client.Complete(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Tools:       []llm.ToolDef{toolDef},
		ToolChoice:  "required",
		Temperature: 0.2,
		MaxTokens:   8192,
	})
	if err != nil {
		r.Status = "llm_fail"
		r.Reason = err.Error()
		r.DurationMs = time.Since(start).Milliseconds()
		return r
	}

	raw, err := agent.EditorEvalExtractToolArgs(resp)
	if err != nil {
		r.Status = "parse_fail"
		r.Reason = err.Error()
		r.DurationMs = time.Since(start).Milliseconds()
		return r
	}
	parsed, err := agent.EditorEvalParseResponse(raw)
	if err != nil {
		r.Status = "parse_fail"
		r.Reason = fmt.Sprintf("parse coder response: %v", err)
		r.DurationMs = time.Since(start).Milliseconds()
		return r
	}

	// Apply the edit.
	if len(parsed.Edits) > 0 {
		if err := agent.ApplyFileEdits(workFile, parsed.Edits); err != nil {
			r.Status = "edit_fail"
			r.Reason = fmt.Sprintf("%v", err)
			r.DurationMs = time.Since(start).Milliseconds()
			return r
		}
	} else if parsed.Code != "" {
		if err := os.WriteFile(workFile, []byte(parsed.Code), 0644); err != nil {
			r.Status = "edit_fail"
			r.Reason = fmt.Sprintf("write full code: %v", err)
			r.DurationMs = time.Since(start).Milliseconds()
			return r
		}
	} else {
		r.Status = "edit_fail"
		r.Reason = "coder returned neither edits nor code"
		r.DurationMs = time.Since(start).Milliseconds()
		return r
	}

	// Read back for assertions.
	edited, err := os.ReadFile(workFile)
	if err != nil {
		r.Status = "edit_fail"
		r.Reason = fmt.Sprintf("read edited: %v", err)
		r.DurationMs = time.Since(start).Milliseconds()
		return r
	}
	editedStr := string(edited)

	if sc.ExpectIn != "" && !strings.Contains(editedStr, sc.ExpectIn) {
		r.Status = "assert_fail"
		r.Reason = fmt.Sprintf("expect_in not found: %q", sc.ExpectIn)
		r.DurationMs = time.Since(start).Milliseconds()
		return r
	}
	if sc.ExpectNotIn != "" && strings.Contains(editedStr, sc.ExpectNotIn) {
		r.Status = "assert_fail"
		r.Reason = fmt.Sprintf("expect_not_in found: %q", sc.ExpectNotIn)
		r.DurationMs = time.Since(start).Milliseconds()
		return r
	}

	// Run the check command, with $FILE exposed.
	if sc.Check != "" {
		cmd := exec.CommandContext(ctx, "bash", "-c", sc.Check)
		// Strip PYTHONPATH — the surrounding shell may pin a different
		// Python version's site-packages, which breaks interpreter-native
		// package imports. Each check gets a clean Python search path.
		env := make([]string, 0, len(os.Environ())+1)
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "PYTHONPATH=") {
				continue
			}
			env = append(env, kv)
		}
		env = append(env, "FILE="+workFile)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			r.Status = "check_fail"
			r.Reason = fmt.Sprintf("%v — %s", err, trim(string(out), 200))
			r.DurationMs = time.Since(start).Milliseconds()
			return r
		}
	}

	r.Status = "pass"
	r.DurationMs = time.Since(start).Milliseconds()
	return r
}

func buildUserPrompt(filename, content, query string) string {
	return fmt.Sprintf(`Edit the file `+"`%s`"+` to: %s

## Current content of `+"`%s`"+`

%s

## Rules
- Return EDITS (old_content → new_content pairs), not a full rewrite, unless the change would affect more than half the file.
- old_content must be a verbatim substring of the current file — every character, including whitespace and punctuation, must match exactly. No paraphrasing.
- Return the "filename" field as %q.
- Preserve everything you are not asked to change.`, filename, query, filename, fenced(content, filename), filename)
}

func fenced(content, filename string) string {
	lang := langFromFilename(filename)
	return fmt.Sprintf("```%s\n%s\n```", lang, content)
}

func langFromFilename(name string) string {
	switch ext := strings.ToLower(filepath.Ext(name)); ext {
	case ".js", ".jsx":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".py":
		return "python"
	case ".go":
		return "go"
	case ".cpp", ".cc", ".cxx":
		return "cpp"
	case ".hpp", ".h":
		return "cpp"
	case ".json":
		return "json"
	case ".yml", ".yaml":
		return "yaml"
	case ".sql":
		return "sql"
	case ".md":
		return "markdown"
	}
	switch name {
	case "Dockerfile":
		return "dockerfile"
	case "CMakeLists.txt":
		return "cmake"
	case "go.mod":
		return "go"
	}
	return ""
}

// ── Output ──────────────────────────────────────────────────────────────────

func summaryLine(r result) {
	mark := "✓"
	if r.Status != "pass" {
		mark = "✗"
	}
	reason := ""
	if r.Reason != "" {
		reason = " — " + trim(r.Reason, 120)
	}
	log.Printf("  %s [%s] %s :: %q%s (%dms)", mark, r.Status, r.Fixture, trim(r.Query, 50), reason, r.DurationMs)
}

func tally(results []result) string {
	counts := map[string]int{}
	for _, r := range results {
		counts[r.Status]++
	}
	keys := []string{"pass", "skip", "llm_fail", "parse_fail", "edit_fail", "check_fail", "assert_fail"}
	var parts []string
	for _, k := range keys {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
		}
	}
	return fmt.Sprintf("%d total (%s)", len(results), strings.Join(parts, ", "))
}

func writeReport(path string, results []result) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(f, "# Editor Eval Report\n\n")
	fmt.Fprintf(f, "Run: %s\n\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(f, "Summary: %s\n\n", tally(results))
	fmt.Fprintf(f, "| Fixture | Query | Status | Reason | ms |\n")
	fmt.Fprintf(f, "|---|---|---|---|--:|\n")
	for _, r := range results {
		reason := strings.ReplaceAll(r.Reason, "|", `\|`)
		reason = strings.ReplaceAll(reason, "\n", " ")
		reason = trim(reason, 200)
		q := strings.ReplaceAll(r.Query, "|", `\|`)
		q = trim(q, 80)
		fmt.Fprintf(f, "| %s | %s | %s | %s | %d |\n", r.Fixture, q, r.Status, reason, r.DurationMs)
	}
	return nil
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func relPath(p string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return p
	}
	rel, err := filepath.Rel(cwd, p)
	if err != nil {
		return p
	}
	return rel
}

