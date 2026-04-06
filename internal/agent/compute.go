package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/user/kaiju/internal/agent/llm"
)

/*
 * runCompute runs the compute pipeline for a compute tool invocation.
 * desc: Dispatches to computePlan (deep mode, initial planning phase) or
 *       computeCode (shallow mode, or deep mode coding phase) based on the
 *       mode and blueprint_ref params. Returns the result string directly —
 *       called via ComputeTool.ExecuteWithContext from the dispatcher.
 *       Param_refs are already resolved by the dispatcher before this runs.
 * param: ec - the execute context (carries Node, Ctx, LLM clients, workspace).
 * param: params - the resolved tool parameters.
 * return: the result JSON string and any error.
 */
func (a *Agent) runCompute(ec *ExecuteContext, params map[string]any) (string, error) {
	n := ec.Node
	n.StartedAt = time.Now()

	goal, _ := params["goal"].(string)
	mode, _ := params["mode"].(string)
	query, _ := params["query"].(string)
	ctxData := params["context"]
	hints, _ := params["hints"].([]any)
	blueprintRef, _ := params["blueprint_ref"].(string)
	lang, _ := params["language"].(string)
	brief, _ := params["brief"].(string)
	structure, _ := params["structure"].(string)

	// Extract task_files (may be []any from JSON)
	var taskFiles []string
	if of, ok := params["task_files"].([]any); ok {
		for _, f := range of {
			if s, ok := f.(string); ok {
				taskFiles = append(taskFiles, s)
			}
		}
	}

	if goal == "" {
		return "", fmt.Errorf("compute node missing goal param")
	}
	if mode == "" {
		mode = "deep"
	}

	tag := sanitizeTag(n.Tag)
	if tag == "" {
		tag = sanitizeTag(goal)
	}
	ts := time.Now().Unix()

	log.Printf("[dag] compute %s: mode=%s goal=%q", n.ID, mode, Text.TruncateLog(goal, 80))

	execute, _ := params["execute"].(string)
	codeCtx := &computeCodeContext{
		brief:      brief,
		structure:  structure,
		taskFiles:  taskFiles,
		interfaces: params["interfaces"],
		execute:    execute,
		service:    params["service"],
	}

	// Guidance sections come from ec.SkillCards, resolved by the dispatcher
	// from whichever cards the classifier picked for this investigation.
	// Every compute node (including parallel coder grafts) sees the same
	// classifier-selected cards, so there's nothing to propagate through params.
	var architectGuidance, coderGuidance string
	if ec.SkillCards != nil {
		architectGuidance = ec.SkillCards["architect"]
		coderGuidance = ec.SkillCards["coder"]
	}

	var result string
	var execErr error

	switch {
	case mode == "deep" && blueprintRef == "":
		sessionID := ""
		if ec.Graph != nil {
			sessionID = ec.Graph.SessionID
		}
		result, execErr = a.computePlan(ec.Ctx, goal, query, ctxData, hints, tag, ts, architectGuidance, sessionID)
	case mode == "deep" && blueprintRef != "":
		result, execErr = a.computeCode(ec.Ctx, goal, query, ctxData, hints, blueprintRef, tag, ts, lang, a.llm, codeCtx, coderGuidance)
	default: // shallow — still uses reasoning model for code quality
		result, execErr = a.computeCode(ec.Ctx, goal, query, ctxData, hints, "", tag, ts, lang, a.llm, codeCtx, coderGuidance)
	}

	n.EndedAt = time.Now()
	return result, execErr
}

/*
 * computePlan runs the deep mode planning phase.
 * desc: Makes one LLM call (reasoning model) to design an implementation approach.
 *       Writes the blueprint to disk and returns a result with type:"blueprint" and a
 *       follow_up spec for the scheduler to graft a coding node.
 */
/*
 * computePlanOutput is the expected JSON structure from the plan LLM.
 */
type computePlanOutput struct {
	Plan       string            `json:"blueprint"`
	Interfaces json.RawMessage   `json:"interfaces,omitempty"`
	Schema     json.RawMessage   `json:"schema,omitempty"`
	Setup      []string          `json:"setup,omitempty"`
	Tasks      []computeWorkItem `json:"tasks"`
	Validation []computeCheck    `json:"validation,omitempty"`
}

type computeWorkItem struct {
	Goal           string          `json:"goal"`
	TaskFiles      []string        `json:"task_files"`
	Brief          string          `json:"brief"`
	Execute        string          `json:"execute,omitempty"`
	Service        *computeService `json:"service,omitempty"`
	DependsOnTasks []int           `json:"depends_on_tasks"`
}

type computeService struct {
	Command string `json:"command"`
	Name    string `json:"name"`
}

/*
 * computeCheck is one validation entry emitted by the architect.
 * desc: After all coder tasks and execute/service grafts complete, the
 *       scheduler grafts one bash node per check that runs the `check`
 *       command. The reflector sees pass/fail as structured evidence of
 *       whether the goal was actually achieved.
 */
type computeCheck struct {
	Name   string `json:"name"`   // short label, used in node tag as verify_<name>
	Check  string `json:"check"`  // shell command to run
	Expect string `json:"expect"` // human-readable success criterion (shown in logs, informational)
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

func (a *Agent) computePlan(ctx context.Context, goal, query string, ctxData any,
	hints []any, tag string, ts int64, architectGuidance, sessionID string) (string, error) {

	// Load session interfaces (API contracts + schema from prior turns)
	ifaces := loadInterfaces(a.cfg.Workspace, sessionID)

	// Deep scan workspace: file tree + small file contents + function signatures
	workspaceScan := scanWorkspaceDeep(a.cfg.Workspace, 4)

	userPrompt := buildComputeUserPrompt(goal, query, ctxData, hints, "")
	if block := formatInterfacesForPrompt(ifaces); block != "" {
		userPrompt += "\n\n" + block
	}
	if workspaceScan != "" {
		userPrompt += "\n\n## Existing Codebase\n" + workspaceScan
	}

	// Include recent worklog so architect knows what's been done before
	worklog := readWorklog(a.cfg.Workspace, 30)
	if worklog != "" {
		userPrompt += "\n\n## Recent Work Log\n```\n" + worklog + "\n```\n"
	}

	// Include existing blueprints so architect can build on previous work
	existingBlueprints := scanExistingBlueprints(a.cfg.Workspace)
	if existingBlueprints != "" {
		userPrompt += "\n\n## Existing Blueprints\n" + existingBlueprints
	}

	systemPrompt := buildComputeArchitectPrompt(architectGuidance)

	resp, err := a.llm.Complete(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.3,
		MaxTokens:   8192,
	})
	if err != nil {
		return "", fmt.Errorf("compute plan LLM: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("compute plan: no LLM choices")
	}

	raw := resp.Choices[0].Message.Content

	// Parse the structured blueprint output
	cleaned := Text.StripCodeFence(raw)
	var planOutput computePlanOutput
	if err := json.Unmarshal([]byte(cleaned), &planOutput); err != nil {
		log.Printf("[dag] compute: JSON parse failed, attempting blueprint extraction: %v", err)
		// Try to extract the blueprint markdown from malformed JSON
		if extracted := extractBlueprint(cleaned); extracted != "" {
			planOutput.Plan = extracted
			log.Printf("[dag] compute: extracted blueprint (%d chars) from malformed response", len(extracted))
		} else {
			log.Printf("[dag] compute: blueprint extraction failed, response is unusable")
			return "", fmt.Errorf("architect returned unparseable response: %w", err)
		}
	}

	// Write blueprint to workspace/blueprints/
	blueprintsDir := filepath.Join(a.cfg.Workspace, "blueprints")
	os.MkdirAll(blueprintsDir, 0755)
	blueprintPath := filepath.Join(blueprintsDir, fmt.Sprintf("%s.blueprint.md", tag))
	if err := os.WriteFile(blueprintPath, []byte(planOutput.Plan), 0644); err != nil {
		return "", fmt.Errorf("write blueprint: %w", err)
	}
	log.Printf("[dag] blueprint written: %s (%d bytes, %d work items)", blueprintPath, len(planOutput.Plan), len(planOutput.Tasks))

	// Merge the architect's interfaces and schema into the session's
	// shared interfaces file. Persists across turns.
	if sessionID != "" {
		var newIfaces, newSchema map[string]any
		if len(planOutput.Interfaces) > 0 {
			_ = json.Unmarshal(planOutput.Interfaces, &newIfaces)
		}
		if len(planOutput.Schema) > 0 {
			_ = json.Unmarshal(planOutput.Schema, &newSchema)
		}
		merged := &sessionInterfaces{Interfaces: newIfaces, Schema: newSchema}
		if err := saveInterfaces(a.cfg.Workspace, sessionID, merged); err != nil {
			log.Printf("[dag] interfaces save failed (non-fatal): %v", err)
		} else {
			log.Printf("[dag] interfaces merged for session %s", sessionID)
		}
	}

	// Store the blueprint path for coders to reference
	planPath := blueprintPath

	// Log plan to worklog
	itemSummary := make([]string, len(planOutput.Tasks))
	for i, item := range planOutput.Tasks {
		itemSummary[i] = fmt.Sprintf("item %d: %s", i, Text.TruncateLog(item.Goal, 60))
	}
	appendWorklog(a.cfg.Workspace, tag, "PLANNED", fmt.Sprintf("%d work items [%s]", len(planOutput.Tasks), strings.Join(itemSummary, "; ")))

	// Build follow-up nodes
	var followUps []map[string]any

	// Include project structure in plan output for coders to reference
	projectStructure := scanWorkspaceTree(a.cfg.Workspace, 3)

	if len(planOutput.Tasks) > 0 {
		// Decomposed: each work item becomes a shallow compute node + file_read nodes
		for i, item := range planOutput.Tasks {
			params := map[string]any{
				"goal":     item.Goal,
				"mode":     "shallow",
				"query":    query,
				"context":  ctxData,
				"blueprint_ref": planPath,
			}
			if len(item.TaskFiles) > 0 {
				params["task_files"] = item.TaskFiles
			}
			if item.Brief != "" {
				params["brief"] = item.Brief
			}
			if projectStructure != "" {
				params["structure"] = projectStructure
			}
			if len(planOutput.Interfaces) > 0 {
				var ifaces any
				json.Unmarshal(planOutput.Interfaces, &ifaces)
				params["interfaces"] = ifaces
			}
			if item.Execute != "" {
				params["execute"] = item.Execute
			}
			if item.Service != nil {
				params["service"] = map[string]string{
					"command": item.Service.Command,
					"name":    item.Service.Name,
				}
			}
			followUp := map[string]any{
				"tool":             "compute",
				"tag":              fmt.Sprintf("%s_%d", tag, i),
				"params":           params,
				"depends_on_tasks": item.DependsOnTasks,
			}
			followUps = append(followUps, followUp)
		}
	} else {
		// Fallback: single code node (old behavior)
		followUps = append(followUps, map[string]any{
			"tool": "compute",
			"tag":  tag + "_code",
			"params": map[string]any{
				"goal":     goal,
				"mode":     "shallow",
				"query":    query,
				"context":  ctxData,
				"blueprint_ref": planPath,
			},
		})
	}

	result := map[string]any{
		"type":       "blueprint",
		"blueprint_ref":   planPath,
		"blueprint":  planOutput.Plan,
		"setup":      planOutput.Setup,
		"follow_up":  followUps,
		"validation": planOutput.Validation,
	}
	out, _ := json.Marshal(result)
	return string(out), nil
}

/*
 * computeCodeContext holds architect-provided context for coding nodes.
 */
type computeCodeContext struct {
	brief      string   // architect's notes for this work item
	structure  string   // project file tree
	taskFiles  []string // files this coder is responsible for
	interfaces any      // API, types, database schemas from architect
	execute    string   // one-shot shell command to run after files are written
	service    any      // long-running service: {"command": "...", "name": "..."}
}

/*
 * computeCode runs the code generation and execution phase.
 * desc: Makes one LLM call to generate code, writes it to disk, executes it,
 *       and parses stdout as JSON. Used by both shallow mode and deep mode
 *       coding phase. Receives architect context (ownership, brief, structure)
 *       when spawned by a deep mode plan.
 */
func (a *Agent) computeCode(ctx context.Context, goal, query string, ctxData any,
	hints []any, blueprintRef, tag string, ts int64, lang string,
	client *llm.Client, codeCtx *computeCodeContext, coderGuidance string) (string, error) {

	// Read plan if provided (deep mode coding phase)
	var plan string
	if blueprintRef != "" {
		planBytes, err := os.ReadFile(blueprintRef)
		if err != nil {
			return "", fmt.Errorf("read plan: %w", err)
		}
		plan = string(planBytes)
	}

	userPrompt := buildComputeUserPrompt(goal, query, ctxData, hints, plan)
	if lang != "" {
		userPrompt += fmt.Sprintf("\n## Preferred Language\n%s\n", lang)
	}

	// Add architect context if provided
	if codeCtx != nil {
		if codeCtx.interfaces != nil {
			ifaceJSON, err := json.MarshalIndent(codeCtx.interfaces, "", "  ")
			if err == nil && string(ifaceJSON) != "null" {
				userPrompt += fmt.Sprintf("\n## Interfaces (implement exactly to spec)\n```json\n%s\n```\n", string(ifaceJSON))
			}
		}
		if codeCtx.brief != "" {
			userPrompt += fmt.Sprintf("\n## Architect Brief\n%s\n", codeCtx.brief)
		}
		if len(codeCtx.taskFiles) > 0 {
			userPrompt += "\n## Your Task Files (write ONLY these)\n"
			for _, f := range codeCtx.taskFiles {
				userPrompt += fmt.Sprintf("- %s\n", f)
			}
		}
		if codeCtx.structure != "" {
			userPrompt += "\n## Project Structure\n" + codeCtx.structure + "\n"
		}
	} else {
		// No architect context — scan workspace for basic awareness
		existingFiles := scanWorkspaceTree(a.cfg.Workspace, 3)
		if existingFiles != "" {
			userPrompt += "\n## Existing Files in Workspace\n" + existingFiles
		}
	}

	// Include recent worklog so coder knows what other nodes have done
	worklog := readWorklog(a.cfg.Workspace, 20)
	if worklog != "" {
		userPrompt += "\n## Recent Work Log\n```\n" + worklog + "\n```\n"
	}

	coderSystem := buildComputeCoderPrompt(coderGuidance)

	resp, err := client.Complete(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: coderSystem},
			{Role: "user", Content: userPrompt},
		},
		Temperature: 0.2,
		MaxTokens:   16384,
	})
	if err != nil {
		return "", fmt.Errorf("compute code LLM: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("compute code: no LLM choices")
	}

	// Parse LLM response: {language, filename, code}
	raw := resp.Choices[0].Message.Content
	var codeResp struct {
		Language string `json:"language"`
		Filename string `json:"filename"`
		Code     string `json:"code"`
	}
	cleaned := Text.StripCodeFence(raw)
	if err := json.Unmarshal([]byte(cleaned), &codeResp); err != nil {
		return "", fmt.Errorf("parse code response: %w (raw: %.300s)", err, raw)
	}
	if codeResp.Code == "" {
		return "", fmt.Errorf("LLM returned empty code")
	}

	if codeResp.Filename == "" {
		codeResp.Filename = fmt.Sprintf("%s_%d.txt", tag, ts)
	}

	// Write generated code directly to workspace. Determine the destination
	// path from task_files (if architect set them) or the coder's filename.
	// Enforce project/ prefix on all paths.
	destPath := codeResp.Filename
	if codeCtx != nil && len(codeCtx.taskFiles) > 0 {
		destPath = codeCtx.taskFiles[0]
	}
	if !strings.HasPrefix(destPath, "project/") {
		destPath = "project/" + destPath
	}
	codePath := filepath.Join(a.cfg.Workspace, destPath)
	os.MkdirAll(filepath.Dir(codePath), 0755)
	if err := os.WriteFile(codePath, []byte(codeResp.Code), 0644); err != nil {
		return "", fmt.Errorf("write code to workspace: %w", err)
	}
	log.Printf("[dag] compute code written: %s (%s, %d bytes)", codePath, codeResp.Language, len(codeResp.Code))
	filesCreated := []string{destPath}

	// Log success to worklog
	appendWorklog(a.cfg.Workspace, tag, "OK", fmt.Sprintf("wrote %s (%s)", strings.Join(filesCreated, ", "), codeResp.Language))

	result := map[string]any{
		"type":          "result",
		"files_created": filesCreated,
		"code_path":     codePath,
		"language":      codeResp.Language,
	}
	if codeCtx != nil && codeCtx.execute != "" {
		// Enforce project/ prefix on execute commands that reference file paths.
		execCmd := codeCtx.execute
		// Heuristic: if the command references a path without project/ prefix
		// and it's not a global command (npm, node -e, curl, etc.), prepend it.
		for _, prefix := range []string{"node ", "python ", "python3 ", "sh ", "bash "} {
			if strings.HasPrefix(execCmd, prefix) {
				rest := strings.TrimPrefix(execCmd, prefix)
				if rest != "" && !strings.HasPrefix(rest, "project/") && !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, "-") {
					execCmd = prefix + "project/" + rest
					log.Printf("[dag] compute: rewriting execute path → %s", execCmd)
				}
				break
			}
		}
		result["execute"] = execCmd
	}
	if codeCtx != nil && codeCtx.service != nil {
		result["service"] = codeCtx.service
	}

	out, _ := json.Marshal(result)
	log.Printf("[dag] compute %s result: %s", tag, Text.TruncateLog(string(out), 200))
	return string(out), nil
}

// ── Worklog ─────────────────────────────────────────────────────────────

const worklogFile = ".worklog"

/*
 * readWorklog reads the shared compute worklog from workspace/code/.worklog.
 * desc: Returns the last N lines of the worklog. Returns empty string if
 *       the file doesn't exist yet (first compute node in this workspace).
 */
func readWorklog(workspace string, maxLines int) string {
	path := filepath.Join(workspace, worklogFile)
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
 * desc: Each entry is a single line with timestamp, tag, status, and details.
 *       Creates the file and parent directory if they don't exist.
 */
func appendWorklog(workspace, tag, status, details string) {
	path := filepath.Join(workspace, worklogFile)

	ts := time.Now().UTC().Format("15:04:05")
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
/*
 * scanExistingBlueprints reads all blueprint files from workspace/blueprints/
 * and returns their contents for the architect to reference.
 */
func scanExistingBlueprints(workspace string) string {
	dir := filepath.Join(workspace, "blueprints")
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

// loadLatestBlueprint reads the most recently modified blueprint file.
func loadLatestBlueprint(workspace string) string {
	dir := filepath.Join(workspace, "blueprints")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var latest string
	var latestTime int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".blueprint.md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Unix() > latestTime {
			latestTime = info.ModTime().Unix()
			latest = filepath.Join(dir, e.Name())
		}
	}
	if latest == "" {
		return ""
	}
	data, err := os.ReadFile(latest)
	if err != nil {
		return ""
	}
	return string(data)
}

func buildComputeUserPrompt(goal, query string, ctxData any, hints []any, plan string) string {
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
		sb.WriteString(fmt.Sprintf("\n## Blueprint (authoritative — follow this exactly)\n%s\n", plan))
	}

	return sb.String()
}

var tagSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

/*
 * sanitizeTag creates a filesystem-safe tag from a string.
 * desc: Replaces non-alphanumeric characters with underscores, truncates to 40 chars.
 */
func sanitizeTag(s string) string {
	if len(s) > 40 {
		s = s[:40]
	}
	s = tagSanitizer.ReplaceAllString(s, "_")
	return strings.Trim(s, "_")
}


/*
 * scanWorkspaceTree returns a lightweight listing of workspace files.
 * desc: Walks the workspace up to maxDepth, returning file paths and sizes.
 *       Skips hidden dirs, node_modules, __pycache__, .git. Limits to 50 files.
 */
func scanWorkspaceTree(root string, maxDepth int) string {
	if root == "" {
		return ""
	}

	var lines []string
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "__pycache__": true,
		".venv": true, "vendor": true, ".cache": true,
	}

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || len(lines) >= 50 {
			return filepath.SkipDir
		}

		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}

		// Check depth
		depth := strings.Count(rel, string(filepath.Separator))
		if depth > maxDepth {
			return filepath.SkipDir
		}

		// Skip hidden and known noisy dirs
		base := filepath.Base(path)
		if strings.HasPrefix(base, ".") && info.IsDir() {
			return filepath.SkipDir
		}
		if info.IsDir() && skipDirs[base] {
			return filepath.SkipDir
		}

		if !info.IsDir() {
			size := info.Size()
			if size < 1024 {
				lines = append(lines, fmt.Sprintf("%s (%d bytes)", rel, size))
			} else {
				lines = append(lines, fmt.Sprintf("%s (%.1fKB)", rel, float64(size)/1024))
			}
		}
		return nil
	})

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
