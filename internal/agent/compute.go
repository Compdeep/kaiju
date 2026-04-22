package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/workspace"
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
	blueprintMode, _ := params["blueprint_mode"].(string) // "follow" (default) or "reference"
	lang, _ := params["language"].(string)
	brief, _ := params["brief"].(string)
	structure, _ := params["structure"].(string)

	// Collect any non-reserved params as additional context. The planner
	// often declares param_refs with arbitrary key names (ports_data,
	// spot_rate_1, ebs_rate, …) to inject upstream results. The dispatcher
	// resolves them and lands each as a top-level params[key]. If we only
	// read params["context"], those injected values are silently dropped.
	ctxData = mergeParamRefsIntoContext(params, ctxData)

	// Extract task_files. Accept both shapes: []any (how JSON-unmarshalled
	// DAG params arrive from the Executive/microplanner) and []string (how
	// programmatic callers like edit_file pass it after their own parsing).
	// Silently dropping one shape would starve the coder of file content.
	var taskFiles []string
	switch of := params["task_files"].(type) {
	case []any:
		for _, f := range of {
			if s, ok := f.(string); ok {
				taskFiles = append(taskFiles, s)
			}
		}
	case []string:
		taskFiles = append(taskFiles, of...)
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

	// Debug: report exactly what compute received so test harnesses and
	// prompt-tuning traces can see whether the planner wired data in
	// correctly. Big red flag = "context_bytes=0 task_files=0" on an edit
	// task — the coder will run blind.
	ctxBytes := 0
	if ctxData != nil {
		if b, err := json.Marshal(ctxData); err == nil {
			ctxBytes = len(b)
		}
	}
	refNames := make([]string, 0, len(n.ParamRefs))
	for k := range n.ParamRefs {
		refNames = append(refNames, k)
	}
	log.Printf("[dag] compute %s: mode=%s goal=%q ctx_bytes=%d task_files=%d param_refs=%v",
		n.ID, mode, Text.TruncateLog(goal, 80), ctxBytes, len(taskFiles), refNames)

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

	// Routing is mode-only. blueprint_ref is an input parameter that either
	// path uses if present, NOT a routing signal. Deep means architect.
	// Shallow means coder. Period.
	switch mode {
	case "deep":
		sessionID := ""
		if ec.Graph != nil {
			sessionID = ec.Graph.SessionID
		}
		result, execErr = a.computePlan(ec.Ctx, ec.Graph, goal, query, ctxData, hints, blueprintRef, blueprintMode, tag, ts, architectGuidance, sessionID)
	default: // shallow
		result, execErr = a.computeCode(ec.Ctx, ec.Graph, goal, query, ctxData, hints, blueprintRef, blueprintMode, tag, ts, lang, a.llm, codeCtx, coderGuidance)
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
	Plan        string            `json:"blueprint"`
	ProjectRoot string            `json:"project_root,omitempty"` // e.g. "project/kaiju_webapp"
	Interfaces  json.RawMessage   `json:"interfaces,omitempty"`
	Schema      json.RawMessage   `json:"schema,omitempty"`
	Setup       []string          `json:"setup,omitempty"`
	Tasks       []computeWorkItem `json:"tasks"`
	Services    []computeService  `json:"services,omitempty"`
	Validation  []computeCheck    `json:"validation,omitempty"`
}

type computeWorkItem struct {
	Goal           string          `json:"goal"`
	TaskFiles      flexStringArray `json:"task_files"`
	Brief          string          `json:"brief"`
	Execute        string          `json:"execute,omitempty"`
	Service        *computeService `json:"service,omitempty"`
	DependsOnTasks []int           `json:"depends_on_tasks"`
}

// flexStringArray accepts both a JSON string and a JSON array of strings.
// LLMs frequently return "path" instead of ["path"] for single-element arrays.
type flexStringArray []string

func (f *flexStringArray) UnmarshalJSON(data []byte) error {
	// Try array first
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*f = arr
		return nil
	}
	// Fall back to single string
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*f = []string{s}
		return nil
	}
	// Give up — return empty
	*f = nil
	return nil
}

type computeService struct {
	Command string `json:"command"`
	Name    string `json:"name"`
	Workdir string `json:"workdir,omitempty"`
	Port    int    `json:"port,omitempty"`
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

// computeSessionID safely extracts the SessionID from a possibly-nil graph.
// Used by compute call sites that may run with or without a parent graph.
func computeSessionID(g *Graph) string {
	if g == nil {
		return ""
	}
	return g.SessionID
}

// projectPrefix returns the project root path, resolved in order:
//   1. graph.ProjectRoot (set by architect)
//   2. common prefix of taskFiles (e.g. "project/kaiju_webapp/" from task_files paths)
//   3. "project/" (legacy fallback)
func projectPrefix(g *Graph, taskFiles []string) string {
	if g != nil && g.ProjectRoot != "" {
		root := g.ProjectRoot
		if !strings.HasSuffix(root, "/") {
			root += "/"
		}
		return root
	}
	if root := rootFromTaskFiles(taskFiles); root != "" {
		return root
	}
	return "project/"
}

// rootFromTaskFiles extracts the project root from task_files paths.
// If all paths share a common prefix deeper than "project/" (e.g.
// "project/kaiju_webapp/"), returns that prefix. Returns "" if no
// consistent root can be determined.
func rootFromTaskFiles(taskFiles []string) string {
	if len(taskFiles) == 0 {
		return ""
	}
	// Split first path: "project/kaiju_webapp/src/main.jsx" → ["project", "kaiju_webapp", "src", "main.jsx"]
	parts := strings.Split(taskFiles[0], "/")
	if len(parts) < 3 || parts[0] != "project" {
		return ""
	}
	// Candidate root is "project/<name>/"
	candidate := parts[0] + "/" + parts[1] + "/"
	// Verify all task_files share this prefix
	for _, tf := range taskFiles[1:] {
		if !strings.HasPrefix(tf, candidate) {
			return ""
		}
	}
	return candidate
}

// graphAlertID safely extracts the trigger AlertID via the graph's gate.
// Used to route LLM trace logs to the per-investigation file.
func graphAlertID(g *Graph) string {
	if g == nil || g.Context == nil || g.Context.trigger == nil {
		return ""
	}
	return g.Context.trigger.AlertID
}

func (a *Agent) computePlan(ctx context.Context, graph *Graph, goal, query string, ctxData any,
	hints []any, blueprintRef, blueprintMode, tag string, ts int64, architectGuidance, sessionID string) (string, error) {

	// Load session interfaces (API contracts + schema from prior turns).
	// This is session-scoped already; not part of ContextGate.
	ifaces := loadInterfaces(a.cfg.MetadataDir, sessionID)

	// If the caller passed an explicit blueprint to refine (e.g. the
	// debugger grafting a "rebuild this" task), load it and feed it to the
	// architect as the structural baseline. The architect treats it as the
	// prior plan to extend or correct, not as a coder follow-along.
	var priorBlueprint string
	if blueprintRef != "" {
		resolved := blueprintRef
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(a.cfg.MetadataDir, "blueprints", resolved)
		}
		if data, err := os.ReadFile(resolved); err == nil {
			priorBlueprint = string(data)
			log.Printf("[blueprint] architect %s refining %s (%d bytes)", tag, filepath.Base(resolved), len(data))
		} else {
			log.Printf("[blueprint] architect %s could not read %s: %v", tag, resolved, err)
		}
	}

	userPrompt := buildComputeUserPrompt(goal, query, ctxData, hints, priorBlueprint, blueprintMode)
	if block := formatInterfacesForPrompt(ifaces); block != "" {
		userPrompt += "\n\n" + block
	}

	// Architect's full context comes from ContextGate now: deep workspace scan,
	// function map, existing blueprints, recent worklog, skill guidance. One
	// call, one place to gate, one place to debug.
	if graph != nil && graph.Context != nil {
		// Sources are ordered by priority: skill guidance first (rules the
		// architect MUST see), then existing blueprints (don't duplicate prior
		// work), then function map and workspace scan (avoid name collisions),
		// then worklog (most-recent context). If the budget is tight, the
		// gate trims from the end — worklog is the first to lose.
		gateResp, gerr := graph.Context.Get(ctx, ContextRequest{
			ReturnSources: Sources(
				SkillGuidance([]string{"Architect"}),
				ExistingBlueprints(),
				FunctionMapSpec(5, 16000),
				WorkspaceDeep(4),
				Worklog(30, "all"),
			),
			MaxBudget: 64000,
		})
		if gerr != nil {
			log.Printf("[dag] computePlan context build failed: %v", gerr)
		} else {
			if scan := gateResp.Sources[SourceWorkspaceDeep]; scan != "" {
				userPrompt += "\n\n## Existing Codebase\n" + scan
			}
			if fm := gateResp.Sources[SourceFunctionMap]; fm != "" {
				userPrompt += "\n\n## Function Map (existing declarations)\n" + fm
			}
			if wl := gateResp.Sources[SourceWorklog]; wl != "" {
				userPrompt += "\n\n## Recent Work Log\n```\n" + wl + "\n```\n"
			}
			if eb := gateResp.Sources[SourceExistingBlueprints]; eb != "" {
				userPrompt += "\n\n## Existing Blueprints\n" + eb
			}
		}
	}

	systemPrompt := buildComputeArchitectPrompt(architectGuidance)

	startedArch := time.Now()
	resp, err := a.llm.Complete(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Tools:       []llm.ToolDef{architectToolDef()},
		ToolChoice:  "required",
		Temperature: 0.3,
		MaxTokens:   8192,
	})

	traceArch := LLMTrace{
		AlertID:  graphAlertID(graph),
		NodeID:   tag,
		NodeType: "compute_architect",
		Tag:      tag,
		Started:  startedArch,
		Input: map[string]string{
			"goal":  goal,
			"query": query,
		},
		System:    systemPrompt,
		User:      userPrompt,
		LatencyMS: time.Since(startedArch).Milliseconds(),
	}

	if err != nil {
		traceArch.Err = err.Error()
		WriteLLMTrace(traceArch)
		return "", fmt.Errorf("compute plan LLM: %w", err)
	}

	raw, err := extractToolArgs(resp)
	if err != nil {
		traceArch.Err = err.Error()
		WriteLLMTrace(traceArch)
		return "", fmt.Errorf("compute plan: %w", err)
	}
	traceArch.Output = raw
	traceArch.TokensIn = resp.Usage.PromptTokens
	traceArch.TokensOut = resp.Usage.CompletionTokens
	WriteLLMTrace(traceArch)

	// Parse the structured blueprint output
	var planOutput computePlanOutput
	if err := ParseLLMJSON(raw, &planOutput); err != nil {
		log.Printf("[dag] compute: JSON parse failed, attempting blueprint extraction: %v", err)
		// Try to extract the blueprint markdown from malformed JSON
		cleaned := CleanLLMJSON(raw)
		if extracted := extractBlueprint(cleaned); extracted != "" {
			planOutput.Plan = extracted
			log.Printf("[dag] compute: extracted blueprint (%d chars) from malformed response", len(extracted))
		} else {
			log.Printf("[dag] compute: blueprint extraction failed, response is unusable")
			return "", fmt.Errorf("architect returned unparseable response: %w", err)
		}
	}

	// Write blueprint to the session-scoped blueprints directory.
	bpDir := blueprintsDir(a.cfg.MetadataDir, sessionID)
	os.MkdirAll(bpDir, 0755)
	blueprintPath := filepath.Join(bpDir, fmt.Sprintf("%s.blueprint.md", tag))
	bpContent := fmt.Sprintf("<!-- Created: %s -->\n\n%s", time.Now().UTC().Format(llmTimeFormat), planOutput.Plan)
	if err := os.WriteFile(blueprintPath, []byte(bpContent), 0644); err != nil {
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
		if err := saveInterfaces(a.cfg.MetadataDir, sessionID, merged); err != nil {
			log.Printf("[dag] interfaces save failed (non-fatal): %v", err)
		} else {
			log.Printf("[dag] interfaces merged for session %s", sessionID)
		}
	}

	log.Printf("[dag] compute: architect returned %d validation entries, %d tasks, %d top-level services", len(planOutput.Validation), len(planOutput.Tasks), len(planOutput.Services))

	// Store the blueprint path for coders to reference
	planPath := blueprintPath

	// Log plan to worklog
	itemSummary := make([]string, len(planOutput.Tasks))
	for i, item := range planOutput.Tasks {
		itemSummary[i] = fmt.Sprintf("item %d: %s", i, Text.TruncateLog(item.Goal, 60))
	}
	appendWorklog(a.cfg.MetadataDir, computeSessionID(graph), tag, "PLANNED", fmt.Sprintf("%d work items [%s]", len(planOutput.Tasks), strings.Join(itemSummary, "; ")))

	// Build follow-up nodes
	var followUps []map[string]any

	// Include project structure in plan output for coders to reference.
	// Direct scan (NOT via ContextGate) is intentional here: this is param
	// data flow (architect → child coder via DAG params), not a prompt build.
	// One scan happens here at architect time and is shared by every coder
	// followup; routing through the gate would either duplicate the scan per
	// coder or require gate state to be readable from architect-time code.
	projectStructure := scanWorkspaceTree(a.cfg.Workspace, 3)

	if len(planOutput.Tasks) > 0 {
		// Decomposed: each work item becomes a shallow compute node + file_read nodes
		for i, item := range planOutput.Tasks {
			params := map[string]any{
				"goal":          item.Goal,
				"mode":          "shallow",
				"query":         query,
				"context":       ctxData,
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
				svc := map[string]any{
					"command": item.Service.Command,
					"name":    item.Service.Name,
				}
				if item.Service.Workdir != "" {
					svc["workdir"] = item.Service.Workdir
				}
				if item.Service.Port > 0 {
					svc["port"] = float64(item.Service.Port)
				}
				params["service"] = svc
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
				"goal":          goal,
				"mode":          "shallow",
				"query":         query,
				"context":       ctxData,
				"blueprint_ref": planPath,
			},
		})
	}

	result := map[string]any{
		"type":          "blueprint",
		"blueprint_ref": planPath,
		"blueprint":     planOutput.Plan,
		"project_root":  planOutput.ProjectRoot,
		"setup":         planOutput.Setup,
		"follow_up":     followUps,
		"validation":    planOutput.Validation,
		"services":      planOutput.Services,
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
func (a *Agent) computeCode(ctx context.Context, graph *Graph, goal, query string, ctxData any,
	hints []any, blueprintRef, blueprintMode, tag string, ts int64, lang string,
	client *llm.Client, codeCtx *computeCodeContext, coderGuidance string) (string, error) {

	// Load blueprint — either from explicit ref or latest on disk.
	var plan string
	if blueprintRef != "" {
		// Sanitize: if blueprint_ref doesn't look like a file path, ignore it.
		if strings.HasPrefix(blueprintRef, "#") || strings.Contains(blueprintRef, "\n") || (!strings.Contains(blueprintRef, "/") && !strings.HasSuffix(blueprintRef, ".md")) {
			log.Printf("[dag] compute: ignoring invalid blueprint_ref %q", Text.TruncateLog(blueprintRef, 80))
			blueprintRef = ""
		}
	}
	if blueprintRef != "" {
		if !filepath.IsAbs(blueprintRef) {
			blueprintRef = filepath.Join(a.cfg.MetadataDir, "blueprints", blueprintRef)
		}
		planBytes, err := os.ReadFile(blueprintRef)
		if err != nil {
			log.Printf("[dag] compute: blueprint_ref %q not found, falling back to latest", blueprintRef)
			blueprintRef = ""
		} else {
			plan = string(planBytes)
			now := time.Now()
			os.Chtimes(blueprintRef, now, now)
			log.Printf("[blueprint] touched %s (used by coder %s)", filepath.Base(blueprintRef), tag)
		}
	}
	// Fallback: if no blueprint_ref provided or it failed, load the latest
	// blueprint via ContextGate so the load goes through the same auditable
	// path as every other prompt-context fetch.
	if plan == "" && graph != nil && graph.Context != nil {
		gateResp, gerr := graph.Context.Get(ctx, ContextRequest{
			ReturnSources: Sources(Blueprint()),
			MaxBudget:     16000,
		})
		if gerr != nil {
			log.Printf("[blueprint] gate fallback failed for coder %s: %v", tag, gerr)
		} else if bp := gateResp.Sources[SourceBlueprint]; bp != "" {
			plan = bp
			log.Printf("[blueprint] auto-loaded latest for coder %s (no explicit ref)", tag)
		}
	}

	userPrompt := buildComputeUserPrompt(goal, query, ctxData, hints, plan, blueprintMode)
	if lang != "" {
		userPrompt += fmt.Sprintf("\n## Preferred Language\n%s\n", lang)
	}

	// Architect context. codeCtx is always non-nil at every call site (the
	// dispatcher constructs it before calling). Fields may individually be empty.
	if codeCtx.interfaces != nil {
		ifaceJSON, err := json.MarshalIndent(codeCtx.interfaces, "", "  ")
		if err == nil && string(ifaceJSON) != "null" {
			userPrompt += fmt.Sprintf("\n## Interfaces (implement exactly to spec)\n```json\n%s\n```\n", string(ifaceJSON))
		}
	}
	if codeCtx.brief != "" {
		userPrompt += fmt.Sprintf("\n## Architect Brief\n%s\n", codeCtx.brief)
	}
	// TODO(compute coder enforcement — Option 3 from the task_files plan):
	//
	// The block below only fires when codeCtx.taskFiles is populated. The
	// caller (microplanner / architect) is responsible for populating it.
	// When the microplanner forgets to set task_files for a single-file
	// edit, the coder runs blind, hallucinates the file content from
	// training memory, and produces text-replacement edits that fail with
	// "old_content not found in file". We've patched the microplanner
	// prompt with a strong "task_files (CRITICAL)" rule + worked examples
	// to make the LLM populate it, but soft enforcement is unreliable.
	//
	// Hard fix (Option 3 from the design discussion): enforce read-before-
	// edit at the DISPATCHER level, the same way Claude Code's Edit tool
	// refuses to run if the file wasn't Read first. Two viable approaches:
	//
	//   3a. Hard fail: at graft time (planStepsToNodes), reject any compute
	//       step where mode == "shallow" AND task_files is empty AND the
	//       goal mentions a file path. The microplanner sees the error and
	//       must re-plan with task_files set. Cleanest enforcement but
	//       creates a re-plan loop.
	//
	//   3b. Auto-extract: if task_files is empty but the goal text contains
	//       a path-shaped substring (regex /[A-Za-z_/.-]+\.(js|ts|jsx|tsx|
	//       json|py|go|md|yml|yaml|toml|sh|html|css)/), auto-populate
	//       task_files from the matches before invoking the coder. More
	//       forgiving, no re-plan needed, but introduces parsing brittleness.
	//
	// Decision: ship the prompt fix first (already done), watch traces for
	// recurrence. If task_files-less compute steps still appear in real
	// runs, implement 3a — it's the more honest enforcement and aligns with
	// Claude Code's tool-level read-before-edit invariant.
	//
	// Until then, this block silently skips the read when task_files is
	// empty, and the coder edits blind.
	if len(codeCtx.taskFiles) > 0 {
		userPrompt += "\n## Your Task Files (write ONLY these)\n"
		for _, f := range codeCtx.taskFiles {
			userPrompt += fmt.Sprintf("- %s\n", f)
		}
		// Edit mode: if the file exists, show content for text-match edits.
		for _, f := range codeCtx.taskFiles {
			targetPath := f
			if !strings.HasPrefix(targetPath, projectPrefix(graph, codeCtx.taskFiles)) {
				targetPath = projectPrefix(graph, codeCtx.taskFiles) + targetPath
			}
			fullPath := filepath.Join(a.cfg.Workspace, targetPath)
			data, readErr := os.ReadFile(fullPath)
			if readErr != nil || len(data) == 0 {
				continue // file doesn't exist or empty — write mode
			}
			userPrompt += "\n## Mode: EDIT (file exists — use old_content/new_content text replacements)\n"
			userPrompt += fmt.Sprintf("\n## Current Content of %s\n```\n%s\n```\n", targetPath, string(data))
		}
	}
	if codeCtx.structure != "" {
		userPrompt += "\n## Project Structure\n" + codeCtx.structure + "\n"
	}

	// Worklog and skill guidance via ContextGate. Skill guidance is the
	// higher-priority source — the coder needs the rules even if the worklog
	// is large. Worklog gets trimmed first when over budget.
	if graph != nil && graph.Context != nil {
		gateResp, gerr := graph.Context.Get(ctx, ContextRequest{
			ReturnSources: Sources(
				SkillGuidance([]string{"Coder"}),
				Worklog(20, "all"),
			),
			MaxBudget: 24000,
		})
		if gerr != nil {
			log.Printf("[dag] computeCode context build failed: %v", gerr)
		} else {
			if wl := gateResp.Sources[SourceWorklog]; wl != "" {
				userPrompt += "\n## Recent Work Log\n```\n" + wl + "\n```\n"
			}
		}
	}

	coderSystem := buildComputeCoderPrompt(coderGuidance)

	startedCode := time.Now()
	resp, err := client.Complete(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: coderSystem},
			{Role: "user", Content: userPrompt},
		},
		Tools:       []llm.ToolDef{coderToolDef()},
		ToolChoice:  "required",
		Temperature: 0.2,
		MaxTokens:   16384,
	})

	traceCode := LLMTrace{
		AlertID:  graphAlertID(graph),
		NodeID:   tag,
		NodeType: "compute_coder",
		Tag:      tag,
		Started:  startedCode,
		Input: map[string]string{
			"goal":          goal,
			"blueprint_ref": blueprintRef,
			"language":      lang,
		},
		System:    coderSystem,
		User:      userPrompt,
		LatencyMS: time.Since(startedCode).Milliseconds(),
	}

	if err != nil {
		traceCode.Err = err.Error()
		WriteLLMTrace(traceCode)
		return "", fmt.Errorf("compute code LLM: %w", err)
	}

	// Parse LLM response — two formats handled by the same tool:
	// Write mode: {language, filename, code} — complete file
	// Edit mode:  {language, filename, edits: [{old_content, new_content}]} — text replacements
	raw, err := extractToolArgs(resp)
	if err != nil {
		traceCode.Err = err.Error()
		WriteLLMTrace(traceCode)
		return "", fmt.Errorf("compute code: %w", err)
	}
	traceCode.Output = raw
	traceCode.TokensIn = resp.Usage.PromptTokens
	traceCode.TokensOut = resp.Usage.CompletionTokens
	WriteLLMTrace(traceCode)

	// Try edit format first (text-match replacements)
	var editResp struct {
		Language   string   `json:"language"`
		Filename   string   `json:"filename"`
		Edits      []EditOp `json:"edits"`
		Execute    string   `json:"execute,omitempty"`
		Validation string   `json:"validation,omitempty"`
	}
	if TryParseLLMJSON(raw, &editResp) && len(editResp.Edits) > 0 {
		destPath := editResp.Filename
		if codeCtx != nil && len(codeCtx.taskFiles) > 0 {
			destPath = codeCtx.taskFiles[0]
		}
		if !strings.HasPrefix(destPath, projectPrefix(graph, codeCtx.taskFiles)) {
			destPath = projectPrefix(graph, codeCtx.taskFiles) + destPath
		}
		codePath, safeErr := workspace.SafeJoin(a.cfg.Workspace, destPath)
		if safeErr != nil {
			return "", fmt.Errorf("compute edit rejected: %w", safeErr)
		}

		if err := ApplyFileEdits(codePath, editResp.Edits); err != nil {
			return "", fmt.Errorf("apply edits to %s: %w", destPath, err)
		}

		log.Printf("[dag] compute edit applied: %s (%s, %d edits)", codePath, editResp.Language, len(editResp.Edits))
		appendWorklog(a.cfg.MetadataDir, computeSessionID(graph), tag, "EDIT", fmt.Sprintf("%s — %d edits applied", destPath, len(editResp.Edits)))

		result := map[string]any{
			"type":         "result",
			"files_edited": []string{destPath},
			"edit_count":   len(editResp.Edits),
			"code_path":    codePath,
			"language":     editResp.Language,
		}
		if codeCtx != nil {
			if codeCtx.execute != "" {
				result["execute"] = codeCtx.execute
			}
			if codeCtx.service != nil {
				result["service"] = codeCtx.service
			}
		}
		// Coder-declared execute wins over the architect default when set —
		// it has the freshest knowledge of how to run the edited file.
		if editResp.Execute != "" {
			result["execute"] = editResp.Execute
		}
		if result["execute"] == nil {
			if guess := defaultExecuteFor(editResp.Language, codePath, tag); guess != "" {
				result["execute"] = guess
			}
		}
		if editResp.Validation != "" {
			result["validation"] = editResp.Validation
		}
		out, _ := json.Marshal(result)
		return string(out), nil
	}

	// Write mode — full file
	var codeResp struct {
		Language   string          `json:"language"`
		Filename   string          `json:"filename"`
		Code       json.RawMessage `json:"code"`
		Execute    string          `json:"execute,omitempty"`
		Validation string          `json:"validation,omitempty"`
	}
	if err := ParseLLMJSON(raw, &codeResp); err != nil {
		return "", fmt.Errorf("parse code response: %w (raw: %.300s)", err, raw)
	}

	// Phase B enforcement — compute(shallow) write-mode invariant.
	//
	// When the caller didn't set task_files, this compute step is implicitly
	// a value-computation intent: the Coder emits a runnable script, the
	// script runs, stdout is captured on .output for downstream param_refs.
	// If the Coder returns write-mode output WITHOUT an execute command,
	// there's no exec graft, no captured stdout, and no .output field on
	// the result. Downstream steps that param_ref .output then hit the
	// dispatcher's silent fallback and receive the compute metadata JSON
	// instead of the intended content — which clobbers target files (the
	// brace_json bug).
	//
	// Reject here so the Executive gets a loud error rather than silent
	// corruption. For known-path file operations the Executive should be
	// using edit_file (which always sets task_files and takes this branch
	// in edit-mode, not write-mode). Architect-spawned coder grafts always
	// set task_files per sub-task, so they're unaffected.
	effectiveExecute := codeResp.Execute
	if effectiveExecute == "" && codeCtx != nil {
		effectiveExecute = codeCtx.execute
	}
	if (codeCtx == nil || len(codeCtx.taskFiles) == 0) && effectiveExecute == "" {
		return "", fmt.Errorf("compute(shallow) write-mode without task_files requires `execute` on the Coder response so .output can be captured; got a bare file-write with no runnable command. For known-path file edits use the edit_file tool instead (raw: %.200s)", raw)
	}
	// Normalize code to string — unwrap JSON string quotes, or re-marshal object to string
	var codeStr string
	if err := json.Unmarshal(codeResp.Code, &codeStr); err != nil {
		var obj any
		if json.Unmarshal(codeResp.Code, &obj) == nil {
			pretty, _ := json.MarshalIndent(obj, "", "  ")
			codeStr = string(pretty)
		} else {
			codeStr = string(codeResp.Code)
		}
	}
	if codeStr == "" {
		// Empty code — coder had nothing to write. Return a no-op result.
		result := map[string]any{"type": "result", "no_changes": true, "reason": "no code changes needed"}
		out, _ := json.Marshal(result)
		log.Printf("[dag] compute %s: LLM returned empty code — treating as no-op", tag)
		return string(out), nil
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
	if !strings.HasPrefix(destPath, projectPrefix(graph, codeCtx.taskFiles)) {
		destPath = projectPrefix(graph, codeCtx.taskFiles) + destPath
	}
	codePath, safeErr := workspace.SafeJoin(a.cfg.Workspace, destPath)
	if safeErr != nil {
		return "", fmt.Errorf("compute write rejected: %w", safeErr)
	}
	os.MkdirAll(filepath.Dir(codePath), 0755)
	if err := OverwriteFile(codePath, codeStr); err != nil {
		return "", fmt.Errorf("write code to workspace: %w", err)
	}
	log.Printf("[dag] compute code written: %s (%s, %d bytes)", codePath, codeResp.Language, len(codeStr))
	filesCreated := []string{destPath}

	// Log success to worklog
	appendWorklog(a.cfg.MetadataDir, computeSessionID(graph), tag, "OK", fmt.Sprintf("wrote %s (%s)", strings.Join(filesCreated, ", "), codeResp.Language))

	result := map[string]any{
		"type":          "result",
		"files_created": filesCreated,
		"code_path":     codePath,
		"language":      codeResp.Language,
	}
	// Execute resolution, priority order:
	//   1. architect-declared codeCtx.execute (deep mode)
	//   2. coder-declared codeResp.Execute    (shallow mode, LLM knows runner)
	//   3. language-based default              (fallback for common langs)
	//
	// All three go through rewriteExecutePath so the file path inside the
	// command matches where we actually wrote the file (e.g. coder says
	// "python3 compute.py" but we wrote to "project/compute.py" — the
	// command needs the same prefix). Runs uniformly in CLI and web modes.
	var execCmd string
	switch {
	case codeCtx != nil && codeCtx.execute != "":
		execCmd = codeCtx.execute
	case codeResp.Execute != "":
		execCmd = codeResp.Execute
	default:
		if guess := defaultExecuteFor(codeResp.Language, codePath, tag); guess != "" {
			log.Printf("[dag] compute %s: coder omitted execute; using language default: %s", tag, guess)
			execCmd = guess
		}
	}
	if execCmd != "" {
		execCmd = rewriteExecutePath(execCmd, projectPrefix(graph, codeCtx.taskFiles))
		// Expose the compute's context to the script at runtime. The coder's
		// system prompt tells it to read $KAIJU_CONTEXT (path to a JSON file)
		// instead of baking literals into the script — which was the source of
		// the "hardcoded stderr blob, then mangled JSON escapes" class of bug.
		if ctxPath := writeContextFile(a.cfg.MetadataDir, computeSessionID(graph), tag, ctxData); ctxPath != "" {
			execCmd = "KAIJU_CONTEXT=" + shQuote(ctxPath) + " " + execCmd
		}
		result["execute"] = execCmd
	}
	if codeCtx != nil && codeCtx.service != nil {
		result["service"] = codeCtx.service
	}
	if codeResp.Validation != "" {
		result["validation"] = codeResp.Validation
	}

	out, _ := json.Marshal(result)
	log.Printf("[dag] compute %s result: %s", tag, Text.TruncateLog(string(out), 200))
	return string(out), nil
}

// defaultExecuteFor returns a reasonable run command for common scripting
// languages when neither the architect nor the coder supplied one. Returns
// "" for languages without a well-known one-shot runner — in that case the
// caller should skip grafting execution rather than guess.
//
// Coders in the new coder prompt are told to emit `execute` explicitly; this
// fallback is just a safety net for the shallow path. A log line fires when
// it triggers so we can audit prompt compliance.
func defaultExecuteFor(language, codePath, tag string) string {
	lang := strings.ToLower(language)
	switch lang {
	case "python", "py":
		return "python3 " + codePath
	case "javascript", "js", "node", "nodejs":
		return "node " + codePath
	case "shell", "sh", "bash":
		return "bash " + codePath
	}
	return ""
}

// writeContextFile serialises the compute's runtime context to a JSON file
// under the session's metadata dir. The execute command sets
// KAIJU_CONTEXT=<path> so the coder's script reads inputs from the file
// instead of hardcoding literals into its source.
//
// Files live under <metadataDir>/sessions/<sessionID>/ctx/ — they get
// cleaned up with the rest of the session state rather than leaking into
// /tmp.
func writeContextFile(metadataDir, sessionID, tag string, ctxData any) string {
	if ctxData == nil {
		return ""
	}
	switch v := ctxData.(type) {
	case map[string]any:
		if len(v) == 0 {
			return ""
		}
	case []any:
		if len(v) == 0 {
			return ""
		}
	}
	body, err := json.Marshal(ctxData)
	if err != nil || len(body) == 0 || string(body) == "null" {
		return ""
	}
	dir := filepath.Join(metadataDir, "sessions", sessionID, "ctx")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ""
	}
	fname := fmt.Sprintf("%s_%d.json", sanitizeTag(tag), time.Now().UnixNano())
	path := filepath.Join(dir, fname)
	if err := os.WriteFile(path, body, 0644); err != nil {
		return ""
	}
	return path
}

// shQuote wraps a string in single quotes for safe inclusion in a shell
// command. Handles embedded single quotes by splitting and rejoining.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// reservedComputeParams lists the param names compute.go itself consumes.
// Anything NOT in this set is treated as upstream data (typically injected
// via planner param_refs) and folded into the compute context so the coder
// can see it.
var reservedComputeParams = map[string]bool{
	"goal":           true,
	"mode":           true,
	"query":          true,
	"context":        true,
	"hints":          true,
	"blueprint_ref":  true,
	"blueprint_mode": true,
	"language":       true,
	"brief":          true,
	"structure":      true,
	"execute":        true,
	"service":        true,
	"interfaces":     true,
	"task_files":     true,
}

// mergeParamRefsIntoContext collects any non-reserved keys from params and
// merges them into ctxData so the coder's "Available Data" section shows
// them. Preserves a pre-existing ctxData if one is present.
func mergeParamRefsIntoContext(params map[string]any, ctxData any) any {
	var extras map[string]any
	for k, v := range params {
		if reservedComputeParams[k] {
			continue
		}
		if v == nil {
			continue
		}
		if extras == nil {
			extras = make(map[string]any)
		}
		extras[k] = v
	}
	if extras == nil {
		return ctxData
	}
	switch ctx := ctxData.(type) {
	case nil:
		return extras
	case map[string]any:
		// Merge without clobbering existing explicit context keys.
		for k, v := range extras {
			if _, exists := ctx[k]; !exists {
				ctx[k] = v
			}
		}
		return ctx
	default:
		return map[string]any{
			"context": ctxData,
			"inputs":  extras,
		}
	}
}

// rewriteExecutePath prepends prefix to a workspace-relative file path
// inside an execute command. Handles the common case where the coder writes
// "python3 compute.py" but the file was actually written to "project/compute.py"
// — we patch the command so it finds the file.
//
// No-op if:
//   - command doesn't start with a known interpreter (node, python, bash, sh)
//   - the referenced path is already absolute (/...)
//   - the referenced path already starts with prefix
//   - the referenced path is a flag (-v, --help)
func rewriteExecutePath(cmd, prefix string) string {
	if prefix == "" || cmd == "" {
		return cmd
	}
	for _, p := range []string{"node ", "python ", "python3 ", "sh ", "bash ", "npx "} {
		if !strings.HasPrefix(cmd, p) {
			continue
		}
		rest := strings.TrimPrefix(cmd, p)
		if rest == "" || strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, "-") || strings.HasPrefix(rest, prefix) {
			return cmd
		}
		rewritten := p + prefix + rest
		log.Printf("[dag] compute: rewriting execute path → %s", rewritten)
		return rewritten
	}
	return cmd
}
