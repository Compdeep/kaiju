package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// looksLikeFailure checks if a validator's output indicates failure despite exit code 0.
// Used for verify_*/revalidate_* nodes where the bash command exited 0 but the
// content reveals the check didn't actually pass.
func looksLikeFailure(output string) bool {
	trimmed := strings.TrimSpace(output)

	// Empty output from a validator = nothing was served = failure
	if trimmed == "" {
		return true
	}

	lower := strings.ToLower(trimmed)

	// Explicit error indicators
	indicators := []string{
		"service not responding",
		"service unavailable",
		"service error log",
		"connection refused",
		"not found",
		"missing script",
		"npm error",
		"command not found",
		"no such file",
		"econnrefused",
		"error:",
		"failed to",
		"cannot find",
		"syntax error",
	}
	for _, ind := range indicators {
		if strings.Contains(lower, ind) {
			return true
		}
	}
	return false
}

// ── LLM JSON parsing ───────────────────────────────────────────────────────
// LLMs return messy JSON: code fences, trailing commas, numbers as strings,
// truncated braces, text before/after the JSON. These utilities centralize
// cleanup so every call site gets the same robustness.

// ParseLLMJSON cleans LLM output and unmarshals into target.
// Strips code fences, finds JSON boundaries, repairs trailing commas
// and unclosed braces, then unmarshals. Returns error on failure.
func ParseLLMJSON(raw string, target any) error {
	cleaned := cleanLLMJSON(raw)
	return json.Unmarshal([]byte(cleaned), target)
}

// TryParseLLMJSON is like ParseLLMJSON but returns (ok bool) instead of error.
// Useful for try-multiple-formats patterns.
func TryParseLLMJSON(raw string, target any) bool {
	return ParseLLMJSON(raw, target) == nil
}

// CleanLLMJSON applies all cleanup steps without unmarshaling.
// Exported for callers that need the cleaned string (e.g. for logging).
func CleanLLMJSON(raw string) string {
	return cleanLLMJSON(raw)
}

// cleanLLMJSON is the internal cleanup pipeline.
func cleanLLMJSON(raw string) string {
	s := strings.TrimSpace(raw)

	// 1. Strip code fences (```json ... ```)
	if strings.HasPrefix(s, "```") {
		idx := strings.Index(s, "\n")
		if idx >= 0 {
			s = s[idx+1:]
		}
	}
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "```" {
			s = strings.Join(lines[:i], "\n")
			break
		}
	}
	s = strings.TrimSpace(s)

	// 2. Find JSON boundaries — skip any leading text ("Here is the JSON:\n{...")
	if !strings.HasPrefix(s, "[") && !strings.HasPrefix(s, "{") {
		bracketIdx := strings.Index(s, "[")
		braceIdx := strings.Index(s, "{")
		startIdx := -1
		if bracketIdx >= 0 && (braceIdx < 0 || bracketIdx < braceIdx) {
			startIdx = bracketIdx
		} else if braceIdx >= 0 {
			startIdx = braceIdx
		}
		if startIdx >= 0 {
			s = s[startIdx:]
		}
	}

	// 3. Strip trailing text after JSON closes ("}\nHere is my explanation...")
	if closer := findJSONEnd(s); closer >= 0 && closer < len(s)-1 {
		s = s[:closer+1]
	}

	// 4. Repair trailing commas before } or ] (common LLM mistake)
	s = trailingCommaRe.ReplaceAllString(s, "$1")

	// 5. Close unclosed braces/brackets (truncated output)
	s = repairUnclosed(s)

	return strings.TrimSpace(s)
}

var trailingCommaRe = regexp.MustCompile(`,\s*([\]}])`)

// findJSONEnd finds the index of the closing brace/bracket that matches
// the first opening brace/bracket. Returns -1 if not found.
func findJSONEnd(s string) int {
	if len(s) == 0 {
		return -1
	}
	open := s[0]
	var close byte
	switch open {
	case '{':
		close = '}'
	case '[':
		close = ']'
	default:
		return -1
	}
	depth := 0
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if c == open {
			depth++
		} else if c == close {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// repairUnclosed adds missing closing braces/brackets for truncated JSON.
func repairUnclosed(s string) string {
	var stack []byte
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) > 0 && stack[len(stack)-1] == c {
				stack = stack[:len(stack)-1]
			}
		}
	}
	// Close unclosed string
	if inString {
		s += `"`
	}
	// Close unclosed braces/brackets in reverse order
	for i := len(stack) - 1; i >= 0; i-- {
		s += string(stack[i])
	}
	return s
}

// flexParseInt parses a json.RawMessage as int, accepting both 0 and "0".
func flexParseInt(data json.RawMessage) (int, error) {
	var i int
	if err := json.Unmarshal(data, &i); err == nil {
		return i, nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		return strconv.Atoi(strings.TrimSpace(s))
	}
	return 0, fmt.Errorf("cannot parse %s as int", string(data))
}

// ── Interfaces: shared API interfaces and schema across turns ─────────────
//
// Single file at workspace/blueprints/interfaces.json, keyed by session ID.
// The architect reads before designing and writes after. Persists across
// turns so multi-step projects share the same contracts.

type sessionInterfaces struct {
	Interfaces map[string]any `json:"interfaces,omitempty"`
	Schema     map[string]any `json:"schema,omitempty"`
}

// interfacesPath returns the path to the shared interfaces file.
func interfacesPath(workspace string) string {
	return filepath.Join(workspace, "blueprints", "interfaces.json")
}

// loadInterfaces reads the interfaces for a session. Returns empty if
// the file doesn't exist or the session has no entry.
func loadInterfaces(workspace, sessionID string) *sessionInterfaces {
	si := &sessionInterfaces{
		Interfaces: make(map[string]any),
		Schema:     make(map[string]any),
	}
	if sessionID == "" {
		return si
	}
	data, err := os.ReadFile(interfacesPath(workspace))
	if err != nil {
		return si
	}
	var all map[string]*sessionInterfaces
	if json.Unmarshal(data, &all) != nil {
		return si
	}
	if entry, ok := all[sessionID]; ok && entry != nil {
		if entry.Interfaces != nil {
			si.Interfaces = entry.Interfaces
		}
		if entry.Schema != nil {
			si.Schema = entry.Schema
		}
	}
	return si
}

// saveInterfaces writes the interfaces for a session (additive merge).
func saveInterfaces(workspace, sessionID string, si *sessionInterfaces) error {
	if sessionID == "" || si == nil {
		return nil
	}
	path := interfacesPath(workspace)
	os.MkdirAll(filepath.Dir(path), 0755)

	// Load existing
	var all map[string]*sessionInterfaces
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &all)
	}
	if all == nil {
		all = make(map[string]*sessionInterfaces)
	}

	// Merge into existing session entry
	existing := all[sessionID]
	if existing == nil {
		existing = &sessionInterfaces{
			Interfaces: make(map[string]any),
			Schema:     make(map[string]any),
		}
	}
	for k, v := range si.Interfaces {
		existing.Interfaces[k] = v
	}
	for k, v := range si.Schema {
		existing.Schema[k] = v
	}
	all[sessionID] = existing

	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// formatInterfacesForPrompt returns a markdown block for the architect prompt.
func formatInterfacesForPrompt(si *sessionInterfaces) string {
	if si == nil || (len(si.Interfaces) == 0 && len(si.Schema) == 0) {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Existing Interfaces (authoritative — do not contradict)\n\n")
	if len(si.Interfaces) > 0 {
		ifaceJSON, _ := json.MarshalIndent(si.Interfaces, "", "  ")
		sb.WriteString("**Interfaces:**\n```json\n")
		sb.Write(ifaceJSON)
		sb.WriteString("\n```\n\n")
	}
	if len(si.Schema) > 0 {
		schemaJSON, _ := json.MarshalIndent(si.Schema, "", "  ")
		sb.WriteString("**Schema:**\n```json\n")
		sb.Write(schemaJSON)
		sb.WriteString("\n```\n\n")
	}
	return sb.String()
}

// ── Worklog ─────────────────────────────────────────────────────────────

const worklogFile = ".worklog"

// worklogPath returns the worklog file path for the given session.
// Empty sessionID returns the legacy global path for backwards compatibility
// with code paths that don't have a session context (kernel heartbeat at idle,
// tests, etc.).
func worklogPath(workspace, sessionID string) string {
	if sessionID == "" {
		return filepath.Join(workspace, worklogFile)
	}
	return filepath.Join(workspace, "sessions", sessionID, "worklog")
}

/*
 * readWorklog reads the worklog for a given session.
 * desc: Returns the last N lines. Returns empty string if the file doesn't
 *       exist yet (first event in this session).
 */
func readWorklog(workspace, sessionID string, maxLines int) string {
	path := worklogPath(workspace, sessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

/*
 * appendWorklog appends an entry to the shared compute worklog.
 * desc: Each entry is a single timestamped line. The worklog is the primary
 *       timeline for the reflector and aggregator — it replaces the scattered
 *       graph evidence, failure lists, and skip lists with one chronological
 *       stream. Writes use O_APPEND which is atomic on Linux for < PIPE_BUF
 *       (4096 bytes), safe for parallel goroutines.
 *
 *       Max detail length varies by status:
 *         - Errors/failures: 500 chars (need full context for debugging)
 *         - OK/resolved: 200 chars (just confirmation)
 */
// rotateServiceLogs rotates all service log files at the start of an investigation.
// Old logs go to .prev so they're available for reference but don't pollute
// the current run's diagnosis. Called once per investigation, not per service start.
func rotateServiceLogs(workspace string) {
	logsDir := filepath.Join(workspace, ".services")
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".log") && !strings.HasSuffix(name, ".prev.log") {
			src := filepath.Join(logsDir, name)
			dst := src + ".prev"
			os.Rename(src, dst)
		}
	}
}

// llmTimeFormat is the timestamp format used everywhere LLMs see timestamps.
// Compact, human-readable, includes date. Same format in worklog, gate context,
// blueprints, and prompts.
const llmTimeFormat = "Jan 02 15:04:05"

// markRunStart appends a run separator to the worklog instead of truncating.
// The reflector sees the marker and knows everything above it is from a prior run.
func markRunStart(workspace, sessionID string) {
	path := worklogPath(workspace, sessionID)
	if sessionID != "" {
		_ = os.MkdirAll(filepath.Dir(path), 0755)
	}
	ts := time.Now().UTC().Format(llmTimeFormat)
	marker := fmt.Sprintf("--- RUN %s ---\n", ts)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(marker)
}

// errorStatuses are worklog statuses that carry stack traces, stderr dumps,
// or other failure detail. For these, the relevant content is at the END of
// the message (newest crash, latest stderr line). We tail-truncate so that
// info survives. For non-error statuses we head-truncate the success summary.
var errorStatuses = map[string]bool{
	"FAILED":          true,
	"BASH_ERROR":      true,
	"GATE_BLOCKED":    true,
	"VALIDATION_FAIL": true,
	"DEBUG_PLAN":      true,
	"BLIND_RETRY":     true,
	"ONESHOT_RETRY":   true,
}

func appendWorklog(workspace, sessionID, tag, status, details string) {
	path := worklogPath(workspace, sessionID)
	if sessionID != "" {
		// Ensure session subdirectory exists. Cheap to call repeatedly.
		_ = os.MkdirAll(filepath.Dir(path), 0755)
	}

	// Truncate based on status type:
	// - Errors → tail-truncate to keep the latest stack frame / current error
	// - Successes → head-truncate (the OK summary is at the start)
	if errorStatuses[status] {
		const errMax = 800
		if len(details) > errMax {
			details = "...(earlier output truncated) " + details[len(details)-errMax:]
		}
	} else {
		const okMax = 200
		if len(details) > okMax {
			details = details[:okMax] + "..."
		}
	}

	// Worklog format is one entry per line. Multi-line errors get folded
	// onto a single line with a visible separator so the error content
	// survives but readWorklog's line-based tail still works.
	details = strings.ReplaceAll(details, "\n", " ↩ ")

	ts := time.Now().UTC().Format(llmTimeFormat)
	entry := fmt.Sprintf("[%s] %s — %s: %s\n", ts, tag, status, details)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(entry)
}

// ── Helpers ──────────────────────────────────────────────────────────────

/*
 * buildComputeUserPrompt assembles the user prompt for compute LLM calls.
 * desc: Includes goal, original user query, upstream context data, previous
 *       failure hints, and implementation plan (if provided).
 */
// blueprintsDir returns the directory holding blueprints for the given
// session. Empty sessionID returns the legacy global directory for backward
// compatibility.
func blueprintsDir(workspace, sessionID string) string {
	if sessionID == "" {
		return filepath.Join(workspace, "blueprints")
	}
	return filepath.Join(workspace, "blueprints", sessionID)
}

/*
 * scanExistingBlueprints reads all blueprint files from the session's blueprint
 * directory and returns their contents for the architect to reference. Returns
 * empty if there are no blueprints. Does NOT cross session boundaries.
 */
func scanExistingBlueprints(workspace, sessionID string) string {
	dir := blueprintsDir(workspace, sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	var sb strings.Builder
	totalBytes := 0
	const maxBytes = 8000

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".blueprint.md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		content := string(data)
		if totalBytes+len(content) > maxBytes {
			content = content[:maxBytes-totalBytes] + "\n...(truncated)"
		}
		sb.WriteString(fmt.Sprintf("### %s\n%s\n\n", e.Name(), content))
		totalBytes += len(content)
		if totalBytes >= maxBytes {
			break
		}
	}

	return sb.String()
}

// extractBlueprint attempts to pull the blueprint markdown string from a
// malformed JSON response. Finds the first "blueprint" key, locates the
// opening quote of its value, then scans character by character respecting
// JSON string escaping until the closing quote. Returns the unescaped
// markdown string, or empty if extraction fails.
func extractBlueprint(raw string) string {
	// Find "blueprint" key — try both old and new key names
	idx := -1
	for _, key := range []string{`"blueprint"`, `"plan"`} {
		if i := strings.Index(raw, key); i >= 0 {
			idx = i + len(key)
			break
		}
	}
	if idx < 0 {
		return ""
	}

	// Skip whitespace and colon after the key
	for idx < len(raw) && (raw[idx] == ' ' || raw[idx] == '\t' || raw[idx] == '\n' || raw[idx] == '\r' || raw[idx] == ':') {
		idx++
	}
	if idx >= len(raw) || raw[idx] != '"' {
		return ""
	}
	idx++ // skip opening quote

	// Scan the JSON string value, handling escapes
	var sb strings.Builder
	for idx < len(raw) {
		ch := raw[idx]
		if ch == '\\' && idx+1 < len(raw) {
			next := raw[idx+1]
			switch next {
			case '"':
				sb.WriteByte('"')
			case '\\':
				sb.WriteByte('\\')
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case '/':
				sb.WriteByte('/')
			default:
				sb.WriteByte('\\')
				sb.WriteByte(next)
			}
			idx += 2
			continue
		}
		if ch == '"' {
			// Closing quote — we're done
			return sb.String()
		}
		sb.WriteByte(ch)
		idx++
	}
	// Reached end without closing quote — return what we have if substantial
	if sb.Len() > 100 {
		return sb.String()
	}
	return ""
}

// latestBlueprintPath returns the path to the most relevant architecture
// blueprint for the given session. Empty sessionID falls back to the legacy
// global blueprints directory.
func latestBlueprintPath(workspace, sessionID string) string {
	dir := blueprintsDir(workspace, sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("[blueprint] failed to read dir %s: %v", dir, err)
		return ""
	}
	log.Printf("[blueprint] scanning %s (%d entries)", dir, len(entries))
	// Collect all blueprints
	type bp struct {
		path string
		size int64
		mod  int64
	}
	var all []bp
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".blueprint.md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		all = append(all, bp{path: filepath.Join(dir, e.Name()), size: info.Size(), mod: info.ModTime().Unix()})
	}
	if len(all) == 0 {
		log.Printf("[blueprint] no blueprints found in %s", dir)
		return ""
	}

	// Separate architecture blueprints from auto-generated ones.
	// Convention: _ prefix = auto-generated (debug plans, internal).
	// Everything else = architecture blueprint (main project plans).
	var main, sub []bp
	for _, b := range all {
		name := filepath.Base(b.path)
		if strings.HasPrefix(name, "_") {
			sub = append(sub, b)
		} else {
			main = append(main, b)
		}
	}

	// Pick from main blueprints (newest wins). Fall back to sub if no main exists.
	candidates := main
	if len(candidates) == 0 {
		candidates = sub
	}

	var pick *bp
	for i := range candidates {
		if pick == nil || candidates[i].mod > pick.mod || (candidates[i].mod == pick.mod && candidates[i].size > pick.size) {
			pick = &candidates[i]
		}
	}

	log.Printf("[blueprint] selected %s (%d bytes, mtime %s)", filepath.Base(pick.path), pick.size, time.Unix(pick.mod, 0).Format("15:04:05"))
	return pick.path
}

// loadLatestBlueprint reads the most relevant architecture blueprint content
// for the given session. Empty sessionID falls back to legacy global directory.
func loadLatestBlueprint(workspace, sessionID string) string {
	path := latestBlueprintPath(workspace, sessionID)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func buildComputeUserPrompt(goal, query string, ctxData any, hints []any, plan, blueprintMode string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("## Goal\n%s\n", goal))

	if query != "" {
		sb.WriteString(fmt.Sprintf("\n## Original User Request\n%s\n", query))
	}

	if ctxData != nil {
		contextJSON, err := json.MarshalIndent(ctxData, "", "  ")
		if err == nil && string(contextJSON) != "null" {
			sb.WriteString(fmt.Sprintf("\n## Available Data (from upstream steps)\n```json\n%s\n```\n", string(contextJSON)))
		}
	}

	if len(hints) > 0 {
		sb.WriteString("\n## Previous Attempts (FAILED — learn from these)\n")
		for i, h := range hints {
			sb.WriteString(fmt.Sprintf("%d. %v\n", i+1, h))
		}
		sb.WriteString("\nTry a DIFFERENT approach if the same method keeps failing.\n")
	}

	if plan != "" {
		if blueprintMode == "reference" {
			sb.WriteString(fmt.Sprintf("\n## Blueprint (REFERENCE — use for context and conventions, but you are building something new)\n%s\n", plan))
		} else {
			sb.WriteString(fmt.Sprintf("\n## Blueprint (FOLLOW — use exact paths, conventions, and structure defined here)\n%s\n", plan))
		}
	}

	return sb.String()
}

var tagSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// sanitizeTag creates a filesystem-safe tag from a string.
// Replaces non-alphanumeric characters with underscores, truncates to 40 chars.
func sanitizeTag(s string) string {
	if len(s) > 40 {
		s = s[:40]
	}
	s = tagSanitizer.ReplaceAllString(s, "_")
	return strings.Trim(s, "_")
}


// skipDirs are directories never entered during workspace scans.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "__pycache__": true,
	".venv": true, "vendor": true, ".cache": true, ".next": true,
	"dist": true, ".nuxt": true, ".services": true,
}

// dirEntry is a file or directory discovered during scanning.
type dirEntry struct {
	Rel   string // relative path from root
	IsDir bool
	Size  int64
}

// scanDir reads one directory level and returns its entries, respecting skip rules.
func scanDir(root, rel string) (dirs []dirEntry, files []dirEntry) {
	abs := filepath.Join(root, rel)
	entries, err := os.ReadDir(abs)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		childRel := filepath.Join(rel, name)
		if e.IsDir() {
			if strings.HasPrefix(name, ".") || skipDirs[name] {
				continue
			}
			dirs = append(dirs, dirEntry{Rel: childRel, IsDir: true})
		} else {
			info, err := e.Info()
			if err != nil {
				continue
			}
			ext := strings.TrimPrefix(filepath.Ext(name), ".")
			if binaryExts[ext] {
				continue
			}
			files = append(files, dirEntry{Rel: childRel, Size: info.Size()})
		}
	}
	return
}

/*
 * scanWorkspaceTree returns a breadth-first, round-robin listing of workspace files.
 * desc: Scans breadth-first so every directory gets representation before any
 *       directory dominates the output. Files are sampled round-robin across
 *       directories — one file per directory per pass — so a single large
 *       directory never blows the budget. Directories appear as entries
 *       so the LLM sees the folder structure.
 * param: root - workspace root path.
 * param: maxDepth - maximum directory depth to descend.
 * return: formatted tree string, one entry per line.
 */
func scanWorkspaceTree(root string, maxDepth int) string {
	if root == "" {
		return ""
	}
	const maxEntries = 120

	var lines []string

	// BFS queue of directories to visit, seeded with root.
	type dirSlot struct {
		rel   string
		depth int
	}
	queue := []dirSlot{{rel: "", depth: 0}}

	// Collect directories and their files breadth-first.
	type dirBucket struct {
		rel   string
		files []dirEntry
	}
	var buckets []dirBucket

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		dirs, files := scanDir(root, cur.rel)

		// Show subdirectories as entries and enqueue them.
		for _, d := range dirs {
			lines = append(lines, d.Rel+"/")
			if cur.depth+1 < maxDepth {
				queue = append(queue, dirSlot{rel: d.Rel, depth: cur.depth + 1})
			}
		}

		if len(files) > 0 {
			buckets = append(buckets, dirBucket{rel: cur.rel, files: files})
		}
	}

	// Round-robin files across all buckets so no single dir dominates.
	indices := make([]int, len(buckets)) // per-bucket cursor
	for len(lines) < maxEntries {
		added := false
		for i := range buckets {
			if indices[i] >= len(buckets[i].files) {
				continue
			}
			f := buckets[i].files[indices[i]]
			indices[i]++
			if f.Size < 1024 {
				lines = append(lines, fmt.Sprintf("%s (%d bytes)", f.Rel, f.Size))
			} else {
				lines = append(lines, fmt.Sprintf("%s (%.1fKB)", f.Rel, float64(f.Size)/1024))
			}
			added = true
			if len(lines) >= maxEntries {
				break
			}
		}
		if !added {
			break // all buckets exhausted
		}
	}

	if len(lines) >= maxEntries {
		lines = append(lines, fmt.Sprintf("... (%d entries shown, more files exist)", maxEntries))
	}

	return strings.Join(lines, "\n")
}

var binaryExts = map[string]bool{
	"exe": true, "bin": true, "wasm": true, "so": true, "dylib": true, "dll": true,
	"jpg": true, "jpeg": true, "png": true, "gif": true, "webp": true, "bmp": true, "ico": true,
	"mp4": true, "webm": true, "mov": true, "avi": true, "mkv": true,
	"mp3": true, "wav": true, "ogg": true, "flac": true,
	"zip": true, "tar": true, "gz": true, "bz2": true, "7z": true, "rar": true,
	"pdf": true, "doc": true, "docx": true, "xls": true, "xlsx": true,
	"ttf": true, "woff": true, "woff2": true, "eot": true,
	"db": true, "sqlite": true, "sqlite3": true,
}

var signaturePatterns = map[string][]string{
	"py":  {"def ", "class ", "import ", "from "},
	"go":  {"func ", "type ", "package "},
	"js":  {"function ", "export ", "class ", "const ", "module.exports"},
	"mjs": {"function ", "export ", "class ", "const "},
	"ts":  {"function ", "export ", "class ", "interface ", "type ", "const "},
	"tsx": {"function ", "export ", "class ", "interface ", "type ", "const "},
	"vue": {"<template", "<script", "export default", "defineProps", "defineEmits"},
	"rb":  {"def ", "class ", "module ", "require "},
	"rs":  {"fn ", "struct ", "enum ", "impl ", "pub ", "mod ", "use "},
	"c":   {"int ", "void ", "char ", "struct ", "#include"},
	"h":   {"int ", "void ", "char ", "struct ", "#include", "#define"},
	"cpp": {"int ", "void ", "class ", "struct ", "#include", "namespace"},
	"java": {"public ", "private ", "protected ", "class ", "interface ", "import "},
	"sh":  {"function ", "#!/"},
}

// ── Function Scanner ────────────────────────────────────────────────────────

// FuncDecl represents a function/class/method declaration found by the scanner.
type FuncDecl struct {
	Name      string `json:"name"`
	StartLine int    `json:"start_line"` // 1-based
	EndLine   int    `json:"end_line"`   // 1-based, 0 if boundary not found
	Context   string `json:"context"`    // 2-3 lines above + declaration line
	FilePath  string `json:"file_path"`  // relative to workspace root
}

// langUsesIndent indicates languages where function boundaries are determined
// by indentation level rather than braces.
var langUsesIndent = map[string]bool{
	"py": true,
}

// langUsesKeywordEnd indicates languages that use keyword-based block endings.
// Value is the end keyword (e.g. "end" for Ruby/Lua).
var langUsesKeywordEnd = map[string]string{
	"rb":  "end",
	"lua": "end",
}

// rubyBlockOpeners are keywords that increase nesting depth in Ruby/Lua.
var rubyBlockOpeners = regexp.MustCompile(`^\s*(?:def|class|module|do|if|unless|while|until|for|case|begin)\b`)

// findFuncEndBrace finds the closing brace for a function starting at startIdx
// (0-based line index). Uses a state machine to skip braces inside strings,
// comments, and template literals. Returns the 0-based line index, or -1.
func findFuncEndBrace(lines []string, startIdx int) int {
	depth := 0
	foundOpen := false
	inSingle := false
	inDouble := false
	inBacktick := false
	inBlockComment := false

	for i := startIdx; i < len(lines); i++ {
		line := lines[i]
		inLineComment := false

		for j := 0; j < len(line); j++ {
			ch := line[j]

			// Skip escaped characters
			if j > 0 && line[j-1] == '\\' {
				continue
			}

			// Block comment state
			if inBlockComment {
				if ch == '*' && j+1 < len(line) && line[j+1] == '/' {
					inBlockComment = false
					j++ // skip the /
				}
				continue
			}
			if inLineComment {
				continue
			}

			// String states
			if inSingle {
				if ch == '\'' {
					inSingle = false
				}
				continue
			}
			if inDouble {
				if ch == '"' {
					inDouble = false
				}
				continue
			}
			if inBacktick {
				if ch == '`' {
					inBacktick = false
				}
				continue
			}

			// Detect string/comment starts
			if ch == '/' && j+1 < len(line) {
				if line[j+1] == '/' {
					inLineComment = true
					continue
				}
				if line[j+1] == '*' {
					inBlockComment = true
					j++
					continue
				}
			}
			if ch == '\'' {
				inSingle = true
				continue
			}
			if ch == '"' {
				inDouble = true
				continue
			}
			if ch == '`' {
				inBacktick = true
				continue
			}

			// Count braces only outside strings/comments
			if ch == '{' {
				depth++
				foundOpen = true
			} else if ch == '}' {
				depth--
				if foundOpen && depth == 0 {
					return i
				}
			}
		}
	}
	return -1
}

// findFuncEndIndent finds the end of an indentation-based function (Python).
// Starting from startIdx, reads the indentation of the first non-empty line
// after the def/class line, then finds where indentation drops back to or
// below the declaration level. Returns 0-based line index, or -1.
func findFuncEndIndent(lines []string, startIdx int) int {
	if startIdx >= len(lines) {
		return -1
	}
	// Get the indentation of the declaration line
	declIndent := countLeadingSpaces(lines[startIdx])

	// Find the first non-empty line after the declaration to get body indent
	bodyIndent := -1
	for i := startIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || trimmed == "#" {
			continue
		}
		bodyIndent = countLeadingSpaces(lines[i])
		break
	}
	if bodyIndent <= declIndent {
		// No indented body found
		return startIdx
	}

	// Scan forward until indentation drops to declaration level or less
	lastBodyLine := startIdx
	for i := startIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue // skip blank lines
		}
		indent := countLeadingSpaces(lines[i])
		if indent <= declIndent {
			break
		}
		lastBodyLine = i
	}
	return lastBodyLine
}

func countLeadingSpaces(s string) int {
	n := 0
	for _, ch := range s {
		if ch == ' ' {
			n++
		} else if ch == '\t' {
			n += 4
		} else {
			break
		}
	}
	return n
}

// findFuncEndKeyword finds the end of a keyword-delimited function (Ruby, Lua).
// Counts block-opening keywords and the end keyword to track depth.
func findFuncEndKeyword(lines []string, startIdx int, endKeyword string) int {
	depth := 1 // the declaration itself opens a block
	for i := startIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if rubyBlockOpeners.MatchString(lines[i]) {
			depth++
		}
		if trimmed == endKeyword {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// FunctionMap is a workspace-wide scan result: relative file path → declarations.
type FunctionMap map[string][]FuncDecl

// EditOp represents a single text replacement returned by the coder LLM.
// The coder specifies the exact text to find and the replacement text.
// Simple string matching — no line numbers, no function scanning.
type EditOp struct {
	OldContent string `json:"old_content"` // exact text to find in the file
	NewContent string `json:"new_content"` // replacement text
}

// ApplyEdits applies text-match replacements to file content.
// Each edit finds old_content exactly and replaces with new_content.
// Returns error if any old_content is not found (no partial matches).
func ApplyEdits(content string, edits []EditOp) (string, error) {
	for _, e := range edits {
		if e.OldContent == "" {
			continue
		}
		if !strings.Contains(content, e.OldContent) {
			// Try trimmed match — LLMs sometimes add/remove trailing whitespace
			trimmed := strings.TrimSpace(e.OldContent)
			if trimmed != "" && strings.Contains(content, trimmed) {
				content = strings.Replace(content, trimmed, e.NewContent, 1)
				continue
			}
			return "", fmt.Errorf("old_content not found in file (first 60 chars: %q)", e.OldContent[:min(60, len(e.OldContent))])
		}
		content = strings.Replace(content, e.OldContent, e.NewContent, 1)
	}
	return content, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// langDeclPatterns maps file extension (without dot) to compiled regexes.
// Group 1 captures the symbol name. Patterns are anchored to line start
// (after optional whitespace) to avoid matching inside strings/comments.
var langDeclPatterns = func() map[string][]*regexp.Regexp {
	c := func(patterns ...string) []*regexp.Regexp {
		out := make([]*regexp.Regexp, len(patterns))
		for i, p := range patterns {
			out[i] = regexp.MustCompile(p)
		}
		return out
	}
	return map[string][]*regexp.Regexp{
		"go": c(
			`^\s*func\s+(?:\([^)]+\)\s+)?(\w+)\s*\(`,
			`^\s*type\s+(\w+)\s+(?:struct|interface)`,
		),
		"py": c(
			`^\s*(?:async\s+)?def\s+(\w+)\s*\(`,
			`^\s*class\s+(\w+)`,
		),
		"js": c(
			`^\s*(?:export\s+)?(?:async\s+)?function\s+(\w+)`,
			`^\s*(?:export\s+)?class\s+(\w+)`,
			`^\s*(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?\(`,
		),
		"ts": c(
			`^\s*(?:export\s+)?(?:async\s+)?function\s+(\w+)`,
			`^\s*(?:export\s+)?class\s+(\w+)`,
			`^\s*(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?\(`,
			`^\s*(?:export\s+)?interface\s+(\w+)`,
			`^\s*(?:export\s+)?type\s+(\w+)\s*=`,
		),
		"tsx": c(
			`^\s*(?:export\s+)?(?:async\s+)?function\s+(\w+)`,
			`^\s*(?:export\s+)?class\s+(\w+)`,
			`^\s*(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?\(`,
			`^\s*(?:export\s+)?interface\s+(\w+)`,
			`^\s*(?:export\s+)?type\s+(\w+)\s*=`,
		),
		"jsx": c(
			`^\s*(?:export\s+)?(?:async\s+)?function\s+(\w+)`,
			`^\s*(?:export\s+)?class\s+(\w+)`,
			`^\s*(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?\(`,
		),
		"mjs": c(
			`^\s*(?:export\s+)?(?:async\s+)?function\s+(\w+)`,
			`^\s*(?:export\s+)?class\s+(\w+)`,
			`^\s*(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?\(`,
		),
		"vue": c(
			`^\s*(?:export\s+)?(?:async\s+)?function\s+(\w+)`,
			`^\s*(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?\(`,
		),
		"rb": c(
			`^\s*def\s+(\w+)`,
			`^\s*class\s+(\w+)`,
			`^\s*module\s+(\w+)`,
		),
		"rs": c(
			`^\s*(?:pub\s+)?(?:async\s+)?fn\s+(\w+)`,
			`^\s*(?:pub\s+)?struct\s+(\w+)`,
			`^\s*(?:pub\s+)?enum\s+(\w+)`,
			`^\s*impl\s+(\w+)`,
		),
		"java": c(
			`(?:public|private|protected)\s+(?:static\s+)?(?:[\w<>\[\]]+\s+)+(\w+)\s*\(`,
			`(?:public\s+)?(?:abstract\s+)?class\s+(\w+)`,
			`(?:public\s+)?interface\s+(\w+)`,
		),
		"c": c(
			`^\s*(?:\w+[\s*]+)+(\w+)\s*\([^)]*\)\s*\{`,
		),
		"h": c(
			`^\s*(?:\w+[\s*]+)+(\w+)\s*\([^)]*\)\s*[;{]`,
			`^\s*#define\s+(\w+)`,
		),
		"cpp": c(
			`^\s*(?:\w+[\s*:]+)+(\w+)\s*\([^)]*\)\s*\{`,
			`^\s*class\s+(\w+)`,
			`^\s*namespace\s+(\w+)`,
		),
		"cs": c(
			`(?:public|private|protected|internal)\s+(?:static\s+)?(?:async\s+)?(?:[\w<>\[\]]+\s+)+(\w+)\s*\(`,
			`(?:public\s+)?class\s+(\w+)`,
		),
		"php": c(
			`^\s*(?:public|private|protected)?\s*function\s+(\w+)`,
			`^\s*class\s+(\w+)`,
		),
		"swift": c(
			`^\s*(?:public\s+|private\s+|internal\s+)?func\s+(\w+)`,
			`^\s*(?:public\s+)?(?:class|struct|enum|protocol)\s+(\w+)`,
		),
		"kt": c(
			`^\s*(?:fun|class|object|interface|data\s+class)\s+(\w+)`,
		),
		"sh": c(
			`^\s*(?:function\s+)?(\w+)\s*\(\s*\)\s*\{`,
		),
		"lua": c(
			`^\s*(?:local\s+)?function\s+(\w+)`,
		),
		"dart": c(
			`^\s*(?:\w+\s+)*(\w+)\s*\([^)]*\)\s*(?:async\s*)?\{`,
			`^\s*class\s+(\w+)`,
		),
	}
}()

// ScanFunctionMap walks the workspace and extracts function/class declarations
// from all recognized source files. maxDepth limits recursion (capped at 5).
func ScanFunctionMap(root string, maxDepth int) FunctionMap {
	if maxDepth > 5 {
		maxDepth = 5
	}
	fm := make(FunctionMap)
	fileCount := 0

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "node_modules" || name == "__pycache__" ||
				name == "vendor" || name == ".venv" || name == ".cache" ||
				name == "dist" || name == "build" || name == ".next" {
				return filepath.SkipDir
			}
			rel, _ := filepath.Rel(root, path)
			if rel != "." && strings.Count(rel, string(os.PathSeparator)) >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if fileCount >= 200 {
			return nil
		}
		ext := strings.TrimPrefix(filepath.Ext(path), ".")
		patterns, ok := langDeclPatterns[ext]
		if !ok {
			return nil
		}
		if binaryExts[ext] {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		lines := strings.Split(string(data), "\n")

		rel, _ := filepath.Rel(root, path)
		var decls []FuncDecl
		for i, line := range lines {
			if len(decls) >= 50 {
				break
			}
			for _, pat := range patterns {
				matches := pat.FindStringSubmatch(line)
				if len(matches) >= 2 {
					// Build context: 3 lines above + the declaration line
					ctxStart := i - 3
					if ctxStart < 0 {
						ctxStart = 0
					}
					ctxLines := lines[ctxStart : i+1]

					// Find function end line — try indent, keyword, or brace based on language
					endLine := 0
					if langUsesIndent[ext] {
						if endIdx := findFuncEndIndent(lines, i); endIdx >= 0 {
							endLine = endIdx + 1
						}
					} else if kw, ok := langUsesKeywordEnd[ext]; ok {
						if endIdx := findFuncEndKeyword(lines, i, kw); endIdx >= 0 {
							endLine = endIdx + 1
						}
					} else {
						if endIdx := findFuncEndBrace(lines, i); endIdx >= 0 {
							endLine = endIdx + 1
						}
					}

					decls = append(decls, FuncDecl{
						Name:      matches[1],
						StartLine: i + 1, // 1-based
						EndLine:   endLine,
						Context:   strings.Join(ctxLines, "\n"),
						FilePath:  rel,
					})
					break // one match per line
				}
			}
		}
		if len(decls) > 0 {
			fm[rel] = decls
			fileCount++
		}
		return nil
	})
	return fm
}

// FormatFunctionMapForPrompt renders the function map as markdown for LLM prompts.
// Caps output at maxBytes to avoid blowing up context.
func FormatFunctionMapForPrompt(fm FunctionMap, maxBytes int) string {
	if len(fm) == 0 {
		return ""
	}
	var sb strings.Builder
	for path, decls := range fm {
		if sb.Len() >= maxBytes {
			break
		}
		sb.WriteString(fmt.Sprintf("### %s\n", path))
		for _, d := range decls {
			// Show just the declaration line (last line of context)
			ctxLines := strings.Split(d.Context, "\n")
			declLine := ctxLines[len(ctxLines)-1]
			endStr := ""
			if d.EndLine > 0 {
				endStr = fmt.Sprintf("-%d", d.EndLine)
			}
			entry := fmt.Sprintf("- `%s` (line %d%s): %s\n", d.Name, d.StartLine, endStr, strings.TrimSpace(declLine))
			if sb.Len()+len(entry) > maxBytes {
				sb.WriteString("...(truncated)\n")
				return sb.String()
			}
			sb.WriteString(entry)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// FormatFileFunctionMap renders declarations for a single file.
func FormatFileFunctionMap(decls []FuncDecl) string {
	if len(decls) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, d := range decls {
		if d.EndLine > 0 {
			sb.WriteString(fmt.Sprintf("- `%s` (lines %d-%d):\n```\n%s\n```\n", d.Name, d.StartLine, d.EndLine, d.Context))
		} else {
			sb.WriteString(fmt.Sprintf("- `%s` (line %d):\n```\n%s\n```\n", d.Name, d.StartLine, d.Context))
		}
	}
	return sb.String()
}

// ── Line-Range Splicer ─────────────────────────────────────────────────────

// SpliceFileEdits applies edits to file content. Edits are sorted bottom-to-top
// (highest start line first) and applied sequentially so line numbers stay valid.
// Lines are 1-based, inclusive on both ends.
// ApplyFileEdits reads a file, applies text-match edits, and writes back.
// Falls back to full rewrite if any edit fails to match.
func ApplyFileEdits(filePath string, edits []EditOp) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", filePath, err)
	}
	result, err := ApplyEdits(string(data), edits)
	if err != nil {
		// If match failed and there's only one edit, fall back to full rewrite
		if len(edits) == 1 && edits[0].NewContent != "" {
			log.Printf("[dag] edit match failed for %s, falling back to full rewrite", filePath)
			result = edits[0].NewContent
		} else {
			return fmt.Errorf("edit %s: %w", filePath, err)
		}
	}
	return os.WriteFile(filePath, []byte(result), 0644)
}

// FindCallSites searches the workspace for references to a function name.
// Returns FuncDecl entries for functions that CONTAIN calls to the target.
// Uses the pre-computed FunctionMap to determine which function contains each call.
func FindCallSites(workspace string, funcName string, fm FunctionMap) []FuncDecl {
	var results []FuncDecl
	searchPattern := funcName + "("

	for relPath, decls := range fm {
		fullPath := filepath.Join(workspace, relPath)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")

		for i, line := range lines {
			if !strings.Contains(line, searchPattern) {
				continue
			}
			lineNo := i + 1 // 1-based
			// Find which function contains this call
			for _, d := range decls {
				if d.Name == funcName {
					continue // skip the function's own declaration
				}
				if d.EndLine > 0 && lineNo >= d.StartLine && lineNo <= d.EndLine {
					results = append(results, d)
					break
				}
			}
		}
	}
	return results
}

// ── Workspace Scanning ─────────────────────────────────────────────────────

/*
 * scanWorkspaceDeep returns a rich view of workspace files for the architect.
 * desc: For small files (< 3KB): includes full content.
 *       For larger files: extracts function/class/export signatures.
 *       Skips binary files and noisy directories.
 *       Returns formatted markdown suitable for an LLM prompt.
 */
func scanWorkspaceDeep(root string, maxDepth int) string {
	if root == "" {
		return ""
	}

	var sb strings.Builder
	fileCount := 0
	totalBytes := 0
	const maxTotalBytes = 32000 // cap total output to ~32KB for prompt budget

	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "__pycache__": true,
		".venv": true, "vendor": true, ".cache": true, ".next": true,
		"dist": true, "build": true, ".nuxt": true,
	}

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || totalBytes >= maxTotalBytes {
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}

		depth := strings.Count(rel, string(filepath.Separator))
		if depth > maxDepth {
			return filepath.SkipDir
		}

		base := filepath.Base(path)
		if strings.HasPrefix(base, ".") && info.IsDir() {
			return filepath.SkipDir
		}
		if info.IsDir() && skipDirs[base] {
			return filepath.SkipDir
		}

		if info.IsDir() || fileCount >= 80 {
			return nil
		}

		ext := strings.TrimPrefix(filepath.Ext(base), ".")
		if binaryExts[ext] {
			return nil
		}

		fileCount++
		size := info.Size()

		if size == 0 {
			sb.WriteString(fmt.Sprintf("### %s (empty)\n\n", rel))
			return nil
		}

		if size < 3072 {
			// Small file: include full content
			data, err := os.ReadFile(path)
			if err != nil {
				sb.WriteString(fmt.Sprintf("### %s (%d bytes, unreadable)\n\n", rel, size))
				return nil
			}
			content := string(data)
			entry := fmt.Sprintf("### %s (%d bytes)\n```%s\n%s\n```\n\n", rel, size, ext, content)
			totalBytes += len(entry)
			sb.WriteString(entry)
		} else {
			// Larger file: extract signatures
			data, err := os.ReadFile(path)
			if err != nil {
				sb.WriteString(fmt.Sprintf("### %s (%.1fKB, unreadable)\n\n", rel, float64(size)/1024))
				return nil
			}
			lines := strings.Split(string(data), "\n")
			patterns := signaturePatterns[ext]
			if patterns == nil {
				// Unknown language — just show line count and first few lines
				preview := lines
				if len(preview) > 10 {
					preview = preview[:10]
				}
				entry := fmt.Sprintf("### %s (%d lines, %.1fKB)\n```%s\n%s\n...\n```\n\n",
					rel, len(lines), float64(size)/1024, ext, strings.Join(preview, "\n"))
				totalBytes += len(entry)
				sb.WriteString(entry)
				return nil
			}

			// Extract matching lines with one line of context after
			var sigs []string
			for i, line := range lines {
				trimmed := strings.TrimSpace(line)
				for _, pat := range patterns {
					if strings.HasPrefix(trimmed, pat) || strings.Contains(line, pat) {
						sig := line
						if i+1 < len(lines) {
							next := strings.TrimSpace(lines[i+1])
							if next != "" && next != "{" && next != "}" {
								sig += "\n" + lines[i+1]
							}
						}
						sigs = append(sigs, sig)
						break
					}
				}
			}

			entry := fmt.Sprintf("### %s (%d lines, %.1fKB) — signatures:\n```%s\n%s\n```\n\n",
				rel, len(lines), float64(size)/1024, ext, strings.Join(sigs, "\n"))
			totalBytes += len(entry)
			sb.WriteString(entry)
		}

		return nil
	})

	return sb.String()
}

func extractJSONField(jsonStr, fieldPath string) (string, error) {
	var data any
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		return "", fmt.Errorf("result is not JSON: %w", err)
	}

	parts := strings.Split(fieldPath, ".")
	current := data
	for _, part := range parts {
		switch m := current.(type) {
		case map[string]any:
			val, ok := m[part]
			if !ok {
				return "", fmt.Errorf("field %q not found", part)
			}
			current = val
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(m) {
				return "", fmt.Errorf("invalid array index %q", part)
			}
			current = m[idx]
		default:
			return "", fmt.Errorf("cannot traverse into %T at %q", current, part)
		}
	}

	switch v := current.(type) {
	case string:
		return v, nil
	default:
		b, _ := json.Marshal(v)
		return string(b), nil
	}
}

/*
 * fixComputeStepParams fixes a common LLM mistake where compute tool params
 * (goal, mode, query) are placed at the step level instead of inside params.
 * desc: Parses the raw JSON, checks each step for misplaced fields, moves them
 *       into params, and re-serializes. Returns original string if no fix needed.
 */
func fixComputeStepParams(raw string) string {
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) != nil {
		return raw
	}
	steps, ok := obj["steps"].([]any)
	if !ok {
		return raw
	}
	changed := false
	computeFields := []string{"goal", "mode", "query", "context", "hints", "language"}
	for _, s := range steps {
		step, ok := s.(map[string]any)
		if !ok {
			continue
		}
		toolName, _ := step["tool"].(string)
		if toolName != "compute" {
			continue
		}
		params, _ := step["params"].(map[string]any)
		if params == nil {
			params = make(map[string]any)
		}
		for _, field := range computeFields {
			if val, exists := step[field]; exists {
				if _, inParams := params[field]; !inParams {
					params[field] = val
				}
				delete(step, field)
				changed = true
			}
		}
		step["params"] = params
	}
	if !changed {
		return raw
	}
	fixed, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	log.Printf("[dag] executive: fixed misplaced compute params in plan() call")
	return string(fixed)
}

// rewriteDependentsMultiExcluding replaces oldID with all replacement node IDs,
// skipping the replacement nodes themselves. Used when compute plan grafts
// multiple child nodes — downstream nodes now depend on ALL children.
func rewriteDependentsMultiExcluding(graph *Graph, oldID string, replacements []*Node) {
	graph.mu.Lock()
	defer graph.mu.Unlock()

	replIDs := make([]string, len(replacements))
	excludeSet := make(map[string]bool, len(replacements))
	for i, r := range replacements {
		replIDs[i] = r.ID
		excludeSet[r.ID] = true
	}

	for _, n := range graph.nodes {
		if n.State != StatePending || excludeSet[n.ID] {
			continue
		}
		for i, dep := range n.DependsOn {
			if dep == oldID {
				newDeps := make([]string, 0, len(n.DependsOn)-1+len(replIDs))
				newDeps = append(newDeps, n.DependsOn[:i]...)
				newDeps = append(newDeps, replIDs...)
				newDeps = append(newDeps, n.DependsOn[i+1:]...)
				n.DependsOn = newDeps
				break
			}
		}
	}
}
