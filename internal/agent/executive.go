package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/gates"
	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/agent/tools"
)

/*
 * prefixAssistantHistory rewrites conversation history for the planner.
 * desc: Keeps user messages as-is. Prefixes assistant messages with
 *       "[Executive Kernel]" so the planner doesn't mistake aggregator/reflector
 *       prose for its own prior output and mimic the format.
 */
func prefixAssistantHistory(history []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(history))
	for _, m := range history {
		if m.Role == "assistant" {
			out = append(out, llm.Message{
				Role:    "assistant",
				Content: "[Executive Kernel] " + m.Content,
			})
		} else {
			out = append(out, m)
		}
	}
	return out
}

/*
 * compileToolIndex builds a compact function-signature-style tool listing.
 * desc: Produces a string like:
 *   web_search(query*, max_results) — Search the web, returns titles/URLs/snippets
 *   bash(command*) — Run any shell command or script
 * Only includes callable tools from the registry (not guidance-only skills).
 * Called once at prompt build time.
 * param: registry - the tool registry
 * param: names - tool names to include
 * return: compiled tool index string
 */
func compileToolIndex(registry *tools.Registry, names []string) string {
	var sb strings.Builder
	sb.WriteString("## Tools (* = required param)\n")
	for _, name := range names {
		skill, ok := registry.Get(name)
		if !ok {
			continue
		}
		sig := compactParamSignature(skill.Parameters())
		sb.WriteString(fmt.Sprintf("%s(%s) — %s\n", name, sig, skill.Description()))
	}
	return sb.String()
}

/*
 * compactParamSignature extracts param names from a JSON schema into a function signature.
 * desc: Parses the tool's parameter JSON schema and produces "query*, max_results"
 *       where * marks required params.
 * param: schema - raw JSON parameter schema
 * return: comma-separated parameter signature string
 */
func compactParamSignature(schema json.RawMessage) string {
	var s struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if json.Unmarshal(schema, &s) != nil || len(s.Properties) == 0 {
		return ""
	}

	reqSet := make(map[string]bool)
	for _, r := range s.Required {
		reqSet[r] = true
	}

	var parts []string
	for name := range s.Properties {
		if reqSet[name] {
			parts = append(parts, name+"*")
		} else {
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, ", ")
}

/*
 * ParamInjection declares that a parameter value should be resolved from
 * a dependency's JSON output at execution time (dependency injection).
 * desc: The planner outputs these as part of plan steps; the scheduler resolves
 *       them before firing the node.
 */
type ParamInjection struct {
	Step     int    `json:"step"`               // index of dependency step (0-based)
	Field    string `json:"field"`              // dot-path into dependency's JSON result (e.g. "user", "host.name")
	Template string `json:"template,omitempty"` // optional: "C:\\Users\\{{value}}\\Downloads" — {{value}} replaced with extracted field
}

// UnmarshalJSON handles LLMs returning step as string ("0") instead of int (0).
func (p *ParamInjection) UnmarshalJSON(data []byte) error {
	type raw struct {
		Step     json.RawMessage `json:"step"`
		Field    string          `json:"field"`
		Template string          `json:"template,omitempty"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	p.Field = r.Field
	p.Template = r.Template
	n, err := flexParseInt(r.Step)
	if err != nil {
		return fmt.Errorf("param_ref step: %w", err)
	}
	p.Step = n
	return nil
}

/*
 * ResolvedInjection is a ParamInjection with the step index replaced by a
 * concrete node ID.
 * desc: Created by planStepsToNodes during DAG construction. Contains the
 *       graph node ID, field path, and optional template.
 */
type ResolvedInjection struct {
	NodeID   string // graph node ID resolved from ParamInjection.Step
	Field    string
	Template string
}

/*
 * FlexInts is a JSON type that accepts both []int and []string.
 * desc: LLMs frequently return depends_on as ["0","1"] instead of [0,1].
 *       This type handles both formats by attempting int parse first, then
 *       string conversion. Non-numeric strings are silently skipped.
 */
type FlexInts []int

/*
 * UnmarshalJSON implements custom JSON unmarshaling for FlexInts.
 * desc: Tries parsing as []int first, then []string with numeric conversion.
 *       Non-numeric strings are skipped. Defaults to nil on complete failure.
 * param: data - the raw JSON bytes.
 * return: error (always nil — gracefully handles all inputs).
 */
func (f *FlexInts) UnmarshalJSON(data []byte) error {
	// Try []int first
	var ints []int
	if err := json.Unmarshal(data, &ints); err == nil {
		*f = ints
		return nil
	}
	// Try []string and convert
	var strs []string
	if err := json.Unmarshal(data, &strs); err == nil {
		result := make([]int, 0, len(strs))
		for _, s := range strs {
			// Try parsing as int
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil {
				// Skip non-numeric strings like "n1" — treat as no deps
				continue
			}
			result = append(result, n)
		}
		*f = result
		return nil
	}
	// Default to empty
	*f = nil
	return nil
}

/*
 * PlanStep is one entry in the planner's JSON output.
 * desc: Contains the tool name, parameters, optional param_refs for
 *       dependency injection, index-based depends_on references, a
 *       human-readable tag, and an optional capability gap declaration.
 */
type PlanStep struct {
	Type      string                    `json:"type,omitempty"` // "tool" (default) or "compute"
	Tool      string                    `json:"tool"`
	Params    map[string]any            `json:"params"`
	ParamRefs map[string]ParamInjection `json:"-"` // dependency injection — populated by custom unmarshal
	DependsOn FlexInts                  `json:"depends_on"`           // index-based references
	Tag       string                    `json:"tag"`
	Gap       string                    `json:"gap,omitempty"` // capability gap: what's needed but unavailable
}

// UnmarshalJSON handles LLMs putting plain values (e.g. timeout_sec: 15) in
// param_refs alongside real injection objects. Plain values are moved to Params.
func (s *PlanStep) UnmarshalJSON(data []byte) error {
	type raw struct {
		Type      string                       `json:"type,omitempty"`
		Tool      string                       `json:"tool"`
		Params    map[string]any               `json:"params"`
		ParamRefs map[string]json.RawMessage   `json:"param_refs,omitempty"`
		DependsOn FlexInts                     `json:"depends_on"`
		Tag       string                       `json:"tag"`
		Gap       string                       `json:"gap,omitempty"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	s.Type = r.Type
	s.Tool = r.Tool
	s.Params = r.Params
	s.DependsOn = r.DependsOn
	s.Tag = r.Tag
	s.Gap = r.Gap
	if s.Params == nil {
		s.Params = make(map[string]any)
	}
	s.ParamRefs = make(map[string]ParamInjection)
	for k, v := range r.ParamRefs {
		var pi ParamInjection
		if err := json.Unmarshal(v, &pi); err == nil && pi.Field != "" {
			s.ParamRefs[k] = pi
		} else {
			// Not a valid ParamInjection — move to Params as a plain value.
			var plain any
			if json.Unmarshal(v, &plain) == nil {
				s.Params[k] = plain
				log.Printf("[dag] plan: moved misplaced param_ref %q to params (not an injection)", k)
			}
		}
	}
	return nil
}

/*
 * executiveOutput wraps the planner's JSON when intent is auto-inferred.
 * desc: When intent is explicit, the planner returns just []PlanStep.
 *       When auto, it may wrap them in {"intent":"...", "steps":[...]}.
 */
type executiveOutput struct {
	Intent string     `json:"intent"`
	Steps  []PlanStep `json:"steps"`
}

/*
 * executiveSystemPrompt returns the system prompt for the initial planner LLM call.
 * desc: Builds the complete planner system prompt including role description,
 *       planning guidance from capability cards and skills, tool definitions
 *       with parameter and output schemas, IGX section, budget limits, and
 *       expanded planner rules. Only the provided tool names are included.
 * param: relevant - slice of tool names visible to the planner.
 * param: dagMode - the DAG execution mode string.
 * param: intent - the intent level string ("auto" or a specific level).
 * return: the fully composed planner system prompt.
 */
func (a *Agent) executiveSystemPrompt(ctx context.Context, graph *Graph, relevant []string, dagMode, intent string) string {
	var sb strings.Builder
	isNative := a.cfg.ExecutiveMode == "native"

	// Per-investigation skill cards now live on the graph. Fall back to
	// the legacy agent field if no graph is provided (defensive).
	cards := []string{}
	if graph != nil && len(graph.ActiveCards) > 0 {
		cards = graph.ActiveCards
	} else {
		cards = a.activeCards
	}

	if isNative {
		sb.WriteString("You are the Executive Kernel of this computer. You serve a dual purpose:\n")
		sb.WriteString("(1) Assist the user with questions, research, and conversation.\n")
		sb.WriteString("(2) Plan and decompose tasks into discrete operations available below.\n")
		sb.WriteString("    Plan in waves — each wave depends on the previous via depends_on.\n")
		sb.WriteString("    Wire data between waves using param_refs.\n\n")
		sb.WriteString("param_refs example: step 0 is web_search, step 1 needs the URL →\n")
		sb.WriteString("  {\"params\":{},\"param_refs\":{\"url\":{\"step\":0,\"field\":\"results.0.url\"}},\"depends_on\":[0]}\n")
		sb.WriteString("param_refs example: step 0 is web_search, step 1 is bash and needs the URL INSIDE a command string → use template:\n")
		sb.WriteString("  {\"params\":{},\"param_refs\":{\"command\":{\"step\":0,\"field\":\"results.0.url\",\"template\":\"yt-dlp -o 'media/%(title)s.%(ext)s' '{{value}}'\"}},\"depends_on\":[0]}\n")
		sb.WriteString("The template field is REQUIRED when injecting a value into the middle of a string. {{value}} is replaced with the extracted field. Without template, the entire param is replaced by the raw value.\n")
		sb.WriteString("param_refs example: step 0 is sysinfo, step 1 is compute →\n")
		sb.WriteString("  {\"type\":\"compute\",\"tool\":\"compute\",\"params\":{\"goal\":\"...\",\"mode\":\"shallow\"},\"param_refs\":{\"context.time\":{\"step\":0,\"field\":\"time\"}},\"depends_on\":[0]}\n\n")
		sb.WriteString("Make good use of tools to gather real data and help the user. If no suitable tool exists, declare a gap.\n\n")
	} else {
		sb.WriteString("You are the Executive Kernel of this computer. ")
		sb.WriteString("Plan and decompose tasks into discrete operations using the tools available on this system. ")
		sb.WriteString("All tool calls pass through an intent and authorization protocol that enforces safety at execution time. ")
		sb.WriteString("If the available tools can accomplish the request, produce the plan. If no suitable tool exists, declare a gap.\n")
		sb.WriteString(roleDescription(a.cfg.NodeRole))
		sb.WriteString("\n\n")
	}

	if isNative {
		// Native mode: inject skill Planning Guidance sections. These are
		// authoritative — if a skill says "use compute deep", the planner
		// must follow that, not substitute file_list/file_read.
		var nativeGuidance []string
		plannerHeadings := []string{"## Planning Guidance", "## RULES"}
		nativeActiveSet := make(map[string]bool, len(cards))
		for _, k := range cards {
			nativeActiveSet[k] = true
		}
		nativeGated := len(nativeActiveSet) > 0
		for name, gs := range a.skillGuidance {
			if nativeGated && !nativeActiveSet[name] {
				continue
			}
			body := gs.Body()
			if body == "" {
				continue
			}
			var parts []string
			for _, heading := range plannerHeadings {
				if section := Text.ExtractSection(body, heading); section != "" {
					parts = append(parts, section)
				}
			}
			if len(parts) > 0 {
				nativeGuidance = append(nativeGuidance, fmt.Sprintf("### %s\n%s", name, strings.Join(parts, "\n\n")))
			}
		}
		if len(nativeGuidance) > 0 {
			sb.WriteString("## Skill Guidance (authoritative — follow these instructions)\n\n")
			sb.WriteString("Skill guidance is authoritative. If a skill says \"use compute deep\", use compute deep. Don't inspect — build.\n\n")
			sb.WriteString(strings.Join(nativeGuidance, "\n\n"))
			sb.WriteString("\n\n")
			log.Printf("[dag] executive (native) injected %d skill guidance sections", len(nativeGuidance))
		} else {
			log.Printf("[dag] executive (native) no skill guidance matched (activeCards=%v, skillGuidance=%d)", cards, len(a.skillGuidance))
		}
	} else {
		// Structured mode: full guidance from capability cards + SkillMD skills.
		// Two sources, cleanly separated:
		//   1. capability cards from cards → Planning Guidance section
		//   2. guidance SkillMDs from a.skillGuidance → Planning Guidance sections
		// Neither comes from the tool list — tools are tools, skills are skills.
		var guidance []string
		if len(cards) > 0 {
			for _, key := range cards {
				card, ok := a.capabilities[key]
				if !ok {
					continue
				}
				if section := Text.ExtractSection(card.Body, "## Planning Guidance"); section != "" {
					guidance = append(guidance, section)
				}
			}
		}
		// Iterate guidance SkillMDs directly. If preflight is active and picked
		// a subset (cards may also name skills), honor it; otherwise
		// include all loaded guidance skills.
		plannerSections := []string{"## Planning Guidance", "## When to Use", "## Approach Selection"}
		activeSet := make(map[string]bool, len(cards))
		for _, k := range cards {
			activeSet[k] = true
		}
		gateActive := len(activeSet) > 0
		for name, gs := range a.skillGuidance {
			if gateActive && !activeSet[name] {
				continue
			}
			body := gs.Body()
			if body == "" {
				continue
			}
			var parts []string
			for _, heading := range plannerSections {
				if section := Text.ExtractSection(body, heading); section != "" {
					parts = append(parts, section)
				}
			}
			if len(parts) > 0 {
				guidance = append(guidance, fmt.Sprintf("### %s\n%s", name, strings.Join(parts, "\n\n")))
			}
		}
		if len(guidance) > 0 {
			sb.WriteString("## Domain & Skill Guidance\n\n")
			for _, g := range guidance {
				sb.WriteString(strings.TrimSpace(g))
				sb.WriteString("\n\n")
			}
		}
	}

	// IGX section
	var igxSection string
	if intent == "auto" {
		igxSection = `Intent is auto-determined. Tools exceeding intent are blocked at execution time.`
	} else {
		igxSection = fmt.Sprintf(`Intent: **%s**. Tools exceeding intent are blocked at execution time.`, intent)
	}

	if isNative {
		sb.WriteString(fmt.Sprintf("%s\n", igxSection))
		sb.WriteString("All tool calls pass through an intent and authorization protocol that enforces safety at execution time.\n")
		sb.WriteString(compileToolIndex(a.registry, relevant))
		sb.WriteString("\n")
	} else {
		sb.WriteString("## Available Tools\nTools available for your plan. Use bash/powershell for any task that can be done from the command line.\n\n")
		for _, name := range relevant {
			skill, ok := a.registry.Get(name)
			if !ok {
				continue
			}
			sb.WriteString(fmt.Sprintf("### %s\n%s\n", name, skill.Description()))
			sb.WriteString(fmt.Sprintf("Parameters: %s\n", string(skill.Parameters())))
			if outSchema := tools.GetOutputSchema(skill); outSchema != nil {
				sb.WriteString(fmt.Sprintf("Output (JSON): %s\n", string(outSchema)))
				var schemaMeta struct {
					Description string `json:"description"`
				}
				if json.Unmarshal(outSchema, &schemaMeta) == nil && schemaMeta.Description != "" {
					sb.WriteString(fmt.Sprintf("Chaining: %s\n\n", schemaMeta.Description))
				} else {
					sb.WriteString("Chainable: use param_refs to extract fields from this tool's output.\n\n")
				}
			} else {
				sb.WriteString("Output: unstructured text. Not chainable via param_refs.\n\n")
			}
		}
	}

	if isNative {
		// Compute Nodes section — mirrors executive.md so native mode has the
		// same compute guidance as structured mode. Without this block the
		// model has no example showing the required `goal` and `mode` fields,
		// and no "ONE compute(deep) node" rule, so it tends to plan multiple
		// compute steps and forget the params on the second one.
		sb.WriteString("## Compute Nodes\n")
		sb.WriteString("Use `compute` (type:\"compute\") for ALL implementation work: building projects, writing multi-file code, scaffolding apps, data processing, calculations, or any task requiring writing and executing code. Provide the GOAL — never write code in bash params or file_write content.\n\n")
		sb.WriteString("The compute architect handles ALL implementation details internally: directory creation, dependency installation, file generation, service startup, and validation. Do NOT plan these as separate bash/service steps — they will conflict with what the architect plans.\n\n")
		sb.WriteString("**Use tools directly when they can do the job. compute is for WRITING code — not for wrapping existing tools in scripts.** If yt-dlp can download a video, use bash. If curl can fetch a file, use bash. If a service needs restarting, use service. Only use compute when you need to CREATE new code that doesn't exist yet.\n\n")
		sb.WriteString("Choose the right level:\n")
		sb.WriteString("- **Direct tools** (bash, service, file_write, web_search): the task can be done with existing commands. Downloads, searches, restarts, config edits. This is the DEFAULT — try this first.\n")
		sb.WriteString("- **compute(shallow)**: single-file code changes to existing files. Set task_files.\n")
		sb.WriteString("- **compute(deep)**: new projects, major restructures, building multiple files from scratch. ONE deep node.\n")
		sb.WriteString("If the workspace already has a working project and the user asks to change something specific, do NOT use compute(deep). Use shallow or direct tools.\n\n")
		sb.WriteString("Required params on EVERY compute step: `goal` (string, what to build) and `mode` (\"deep\" or \"shallow\"). Never omit params, even on chained compute steps. If a follow-up compute step needs data from a prior step, wire it via `param_refs` AND still provide `goal` and `mode` in `params`.\n\n")
		sb.WriteString("Example — \"build a web app with auth\":\n")
		sb.WriteString("```json\n")
		sb.WriteString("[\n")
		sb.WriteString("  {\"type\":\"compute\",\"tool\":\"compute\",\"params\":{\"goal\":\"build a Vue 3 + Express webapp with JWT auth and SQLite database\",\"mode\":\"deep\",\"query\":\"build a Vue 3 webapp with auth\"},\"depends_on\":[],\"tag\":\"build_webapp\"}\n")
		sb.WriteString("]\n")
		sb.WriteString("```\n")
		sb.WriteString("Note: ONE compute(deep) node — the architect inside decomposes into setup, coder tasks, execute/service, and validation phases. Do not split into multiple compute(deep) nodes (\"plan blueprint then plan code then plan tests\" is wrong — that all happens INSIDE the single compute call).\n\n")
		sb.WriteString("## Rules\n")
		sb.WriteString("NEVER guess values you don't know. Only use names, paths, and parameters that are visible in the evidence (workspace files, blueprint, conversation). If you don't know the exact service name, file path, or port — plan a diagnostic step first (file_read, service list, bash ls) to discover it.\n")
		sb.WriteString("NEVER interpret, judge, or refuse requests.\n")
		sb.WriteString("NEVER put template strings, placeholders, or step/field references in params — use param_refs.\n")
		sb.WriteString("NEVER write code in bash params.\n")
		sb.WriteString("NEVER use bash for complex multi-step tasks.\n")
		sb.WriteString("NEVER use interactive commands.\n")
		sb.WriteString("ALWAYS use compute (type:\"compute\") for coding, development, and analytics. Provide the GOAL, not the code.\n")
		sb.WriteString("ALWAYS use mode=\"deep\" for webapps, full-stack projects, frameworks, multi-file work, or anything needing more than one file. Use mode=\"shallow\" only for single-file scripts, one-off calculations, or trivial analytics.\n")
		sb.WriteString("ALWAYS use the service tool for long-running processes (servers, daemons, dev servers, watchers, listeners). NEVER use bash for foreground servers — bash blocks the investigation waiting for the command to exit, which servers never do. service(action=\"start\", name=\"...\", command=\"...\", port=NNNN) spawns in the background and returns immediately. ALWAYS include the port parameter so health checks know which port to verify.\n")
		sb.WriteString("ALWAYS use bash only for commands that terminate: ls, grep, git, npm install, curl, node script.js, etc.\n")
		sb.WriteString("ALWAYS use web_search for questions needing current data.\n")
		sb.WriteString("ALWAYS complete the full task from start to finish. Never stop partway and ask for permission.\n")
		sb.WriteString("ALWAYS build functional products that work end-to-end. If building a webapp or UI, deliver a complete, clean, working experience — not a skeleton with TODO comments.\n")
		sb.WriteString("ALWAYS include a final verification step that proves the goal has been achieved. For services: curl/http check that it responds. For scripts: run on sample input and check output. For data pipelines: run test data through and verify result shape. Never end a plan without verification — 'wrote the files' is not achievement.\n")
		sb.WriteString("\n## Workspace Layout\n")
		sb.WriteString("- project/ — source code, application files\n")
		sb.WriteString("- media/ — downloaded media (images, videos, audio). ALWAYS save downloads here: yt-dlp -o 'media/%(title)s.%(ext)s', curl -o media/file.jpg, etc.\n")
		sb.WriteString("- blueprints/ — architecture blueprints (auto-managed by compute)\n")
		sb.WriteString("- canvas/ — user-facing visual content\n")
		// Workspace tree and blueprint via ContextGate. This is the single
		// place project context flows in for the executive — auditable and
		// gateable. ContextGate's WorkspaceTree returns a fenced tree string.
		if graph != nil && graph.Context != nil {
			// Determine if the current query relates to an existing project.
			// Project-building skills (webdeveloper, data_science) indicate the
			// query is about development work that would reference a blueprint.
			// Utility skills (download, web_research, etc.) are unrelated — don't
			// inject the blueprint or it will bias the executive.
			projectSkillActive := false
			projectSkills := map[string]bool{"webdeveloper": true, "data_science": true}
			for _, card := range cards {
				if projectSkills[card] {
					projectSkillActive = true
					break
				}
			}

			// Always load the workspace tree (orientation). Only load the
			// blueprint when a project-building skill is active.
			var gateSources []SourceSpec
			gateSources = append(gateSources, WorkspaceTree(3))
			if projectSkillActive {
				gateSources = append(gateSources, Blueprint())
			}

			gateResp, gerr := graph.Context.Get(ctx, ContextRequest{
				ReturnSources: gateSources,
				MaxBudget:     10000,
			})
			if gerr != nil {
				log.Printf("[dag] executive context build failed: %v", gerr)
			} else {
				if tree := gateResp.Sources[SourceWorkspaceTree]; tree != "" {
					sb.WriteString("\n### Current files\n")
					sb.WriteString(tree)
					sb.WriteString("\n")
				}
				if projectSkillActive {
					if bp := gateResp.Sources[SourceBlueprint]; bp != "" {
						sb.WriteString("\n## Existing Project\n")
						sb.WriteString("An architecture blueprint already exists. Before planning, decide:\n")
						sb.WriteString("1. Is the user asking to FIX or DEBUG the existing project? → Do NOT create a new blueprint. Plan diagnostic and repair steps using existing files.\n")
						sb.WriteString("2. Is the user asking to ADD or EXTEND? → Use compute(mode=\"deep\") ONLY for the new feature. Reference the existing blueprint.\n")
						sb.WriteString("3. Is the user asking to BUILD something completely new and unrelated? → Create a fresh blueprint from scratch.\n")
						sb.WriteString("Most requests about an existing project are case 1 or 2. Only choose case 3 if the user explicitly wants something NEW.\n")
						sb.WriteString("\n### Existing Blueprint Summary\n")
						goalSection := Text.ExtractSection(bp, "## Goal")
						archSection := Text.ExtractSection(bp, "## Architecture")
						dirSection := Text.ExtractSection(bp, "## Directory Structure")
						if goalSection != "" {
							sb.WriteString("**Goal**: " + goalSection + "\n")
						}
						if archSection != "" {
							sb.WriteString("**Architecture**: " + archSection + "\n")
						}
						if dirSection != "" {
							sb.WriteString("**Structure**:\n" + dirSection + "\n")
						}
					}
				}
			}
		}

		sb.WriteString(fmt.Sprintf("\nBudget: max %d steps, %d LLM calls.\n", a.cfg.MaxNodes, a.cfg.MaxLLMCalls))
	} else {
		// Structured mode: full planner.md with format rules, examples, etc.
		// Intent descriptions come from the configurable registry.
		intentBlock := a.intentRegistry.PromptBlock(-1)
		igxFullSection := ""
		if intent == "auto" {
			igxFullSection = "## Intent-Gated Execution (IGX)\n\n" +
				"Intent is auto-determined from your plan. Choose tools matching the query's needs:\n" +
				intentBlock +
				"\nThe system enforces: tool.Impact ≤ min(intent, clearance). Tools exceeding intent WILL BE BLOCKED."
		} else {
			igxFullSection = fmt.Sprintf("## Intent-Gated Execution (IGX)\n\n"+
				"This investigation runs at **%s** intent (operator-enforced, do not override).\n\n"+
				"Available intent levels:\n%s\n"+
				"The system enforces: tool.Impact ≤ min(intent, clearance). Tools exceeding intent WILL BE BLOCKED.", intent, intentBlock)
		}

		rules := expandPlannerTemplate(a.executivePrompt, map[string]string{
			"node_id":       a.cfg.NodeID,
			"max_nodes":     fmt.Sprintf("%d", a.cfg.MaxNodes),
			"max_per_skill": fmt.Sprintf("%d", a.cfg.MaxPerSkill),
			"max_llm_calls": fmt.Sprintf("%d", a.cfg.MaxLLMCalls),
			"dag_mode":      dagMode,
			"batch_size":    fmt.Sprintf("%d", a.cfg.BatchSize),
			"intent":        intent,
			"igx_section":   igxFullSection,
		})
		sb.WriteString(rules)
	}

	// Preflight hints: required tool categories the plan MUST include.
	// Populated by the pre-plan preflight call in scheduler.go.
	if a.preflight != nil && len(a.preflight.RequiredCategories) > 0 {
		sb.WriteString("\n## Required Tool Categories\n")
		sb.WriteString(fmt.Sprintf("This query needs tools from: %s. Your plan MUST include at least one tool from each of these categories. If none exist, declare a gap.\n",
			strings.Join(a.preflight.RequiredCategories, ", ")))
		sb.WriteString("Category → common tools:\n")
		sb.WriteString("- network: web_fetch, web_search\n")
		sb.WriteString("- filesystem: file_read, file_write, file_list\n")
		sb.WriteString("- compute: compute\n")
		sb.WriteString("- process: process_list, process_kill, bash\n")
		sb.WriteString("- info: sysinfo, env_list, disk_usage, net_info\n\n")
	}

	rolePrompt := sb.String() + a.fleetSection()
	return rolePrompt
}

/*
 * PlanResult contains the planner output: steps and optionally an inferred intent.
 * desc: Wraps the parsed plan steps, declared capability gaps, inferred intent
 *       level, and whether intent was auto-inferred.
 */
type PlanResult struct {
	Steps          []PlanStep
	Gaps           []string     // capability gaps declared by the planner
	InferredIntent gates.Intent // only set when intent was auto-inferred
	WasAuto        bool         // true if the planner inferred intent
}

/*
 * ExecutiveConversationalError is returned when the planner responds with
 * conversational text instead of a JSON plan.
 * desc: The Text field contains the planner's response which can be returned
 *       directly to the user as a chat response.
 */
type ExecutiveConversationalError struct {
	Text string
}

/*
 * Error returns the error message for ExecutiveConversationalError.
 * desc: Implements the error interface.
 * return: fixed error string.
 */
func (e *ExecutiveConversationalError) Error() string {
	return "planner returned conversational text instead of JSON plan"
}

/*
 * runExecutive makes a single LLM call to produce the initial investigation plan.
 * desc: When the trigger intent is Auto, the planner also infers the appropriate
 *       intent. Filters relevant skills, builds the planner prompt, sends the
 *       LLM call (with optional retry on prose output), then validates, filters,
 *       and deduplicates the resulting steps.
 * param: ctx - context for the LLM call.
 * param: trigger - the investigation trigger.
 * return: PlanResult pointer with steps and intent, or error.
 */
/*
 * runExecutive dispatches to the appropriate planner mode.
 * desc: Routes to structured (text JSON) or native (function calling) planner
 *       based on agent config. Both return the same PlanResult.
 * param: ctx - context for the LLM call.
 * param: trigger - the investigation trigger.
 * return: PlanResult pointer with steps and intent, or error.
 */
func (a *Agent) runExecutive(ctx context.Context, trigger Trigger, graph *Graph) (*PlanResult, error) {
	if a.cfg.ExecutiveMode == "native" {
		return a.runExecutiveNative(ctx, trigger, graph)
	}
	return a.runExecutiveStructured(ctx, trigger, graph)
}

/*
 * runExecutiveStructured makes a single LLM call to produce a plan via text JSON output.
 * desc: The original planner mode. Sends a prompt asking the LLM to respond with a JSON
 *       array. Parses the text output, handles markdown fences, retries on prose.
 * param: ctx - context for the LLM call.
 * param: trigger - the investigation trigger.
 * return: PlanResult pointer with steps and intent, or error.
 */
func (a *Agent) runExecutiveStructured(ctx context.Context, trigger Trigger, graph *Graph) (*PlanResult, error) {
	relevant := a.relevantTools(ctx, formatTrigger(trigger), trigger.Scope)
	log.Printf("[dag] executive sees %d tools: %v", len(relevant), relevant)
	if len(a.skillGuidance) > 0 {
		log.Printf("[dag] executive has %d guidance skills loaded", len(a.skillGuidance))
	}

	// Resolve DAG mode and intent for planner prompt
	dagMode := a.cfg.DAGMode
	if trigger.DAGMode != "" {
		dagMode = trigger.DAGMode
	}
	intent := trigger.Intent().String()
	// Preflight override: if trigger intent is Auto and preflight provided an intent,
	// use the preflight-classified intent instead of forcing the planner to infer.
	if trigger.Intent() == gates.IntentAuto && a.preflight != nil {
		intent = a.preflight.Intent.String()
		log.Printf("[dag] executive intent from preflight: %s", intent)
	}

	// Planner gets conversation history with assistant messages prefixed
	// as Executive Kernel output. This preserves multi-turn context
	// ("the tools you mentioned") while preventing the planner from
	// mimicking the aggregator's prose style — the prefix signals
	// "this wasn't my output" so the planner continues using plan().
	executiveHistory := prefixAssistantHistory(trigger.History)
	sysPrompt := a.executiveSystemPrompt(ctx, graph, relevant, dagMode, intent)
	userQuery := formatTrigger(trigger)
	// Inject preflight context framing right after the query so the executive
	// understands vague follow-ups like "get more" or "try again."
	if a.preflight != nil && a.preflight.Context != "" {
		userQuery += "\n\n## Context\n" + a.preflight.Context
	}
	messages := BuildMessagesWithHistory(sysPrompt, userQuery, executiveHistory)

	started := time.Now()
	resp, err := a.llm.Complete(ctx, &llm.ChatRequest{
		Messages:    messages,
		Temperature: a.cfg.Temperature,
		MaxTokens:   a.cfg.MaxTokens,
	})

	trace := LLMTrace{
		AlertID:  trigger.AlertID,
		NodeID:   "executive",
		NodeType: "executive_structured",
		Tag:      "plan",
		Started:  started,
		Input: map[string]string{
			"dag_mode": dagMode,
			"intent":   intent,
		},
		System:    sysPrompt,
		User:      userQuery,
		LatencyMS: time.Since(started).Milliseconds(),
	}

	if err != nil {
		trace.Err = err.Error()
		WriteLLMTrace(trace)
		return nil, fmt.Errorf("planner LLM call: %w", err)
	}

	if len(resp.Choices) == 0 {
		trace.Err = "no choices"
		WriteLLMTrace(trace)
		return nil, fmt.Errorf("planner LLM returned no choices")
	}

	raw := resp.Choices[0].Message.Content
	trace.Output = raw
	trace.TokensIn = resp.Usage.PromptTokens
	trace.TokensOut = resp.Usage.CompletionTokens
	WriteLLMTrace(trace)

	log.Printf("[dag] executive output: %s", Text.TruncateLog(raw, 300))
	log.Printf("[dag] executive full output length: %d bytes", len(raw))

	isAuto := trigger.Intent() == gates.IntentAuto
	steps, inferredIntent, err := a.parseExecutiveOutput(raw, isAuto)
	if err != nil {
		// If planner returned prose, retry once with a forceful nudge
		cleaned := strings.TrimSpace(raw)
		if len(cleaned) > 0 && cleaned[0] != '[' && cleaned[0] != '{' {
			log.Printf("[dag] executive returned prose, retrying with JSON nudge")

			// Append the prose response + a nudge as follow-up messages
			retryMessages := append(messages,
				llm.Message{Role: "assistant", Content: raw},
				llm.Message{Role: "user", Content: "That was not valid JSON. You MUST respond with ONLY a JSON array of steps. Start with [ and end with ]. No prose, no explanation. Just the JSON plan."},
			)

			retryResp, retryErr := a.llm.Complete(ctx, &llm.ChatRequest{
				Messages:    retryMessages,
				Temperature: 0.1, // lower temp for more deterministic output
				MaxTokens:   a.cfg.MaxTokens,
			})
			if retryErr == nil && len(retryResp.Choices) > 0 {
				retryRaw := retryResp.Choices[0].Message.Content
				log.Printf("[dag] executive retry output: %s", Text.TruncateLog(retryRaw, 300))
				retrySteps, retryIntent, retryParseErr := a.parseExecutiveOutput(retryRaw, isAuto)
				if retryParseErr == nil {
					steps = retrySteps
					inferredIntent = retryIntent
					err = nil
				}
			}

			// If retry also failed, surface as conversational
			if err != nil {
				log.Printf("[dag] executive retry also failed, surfacing as reply")
				return nil, &ExecutiveConversationalError{Text: cleaned}
			}
		} else {
			return nil, fmt.Errorf("parse planner output: %w", err)
		}
	}

	// Shared validation: filter gaps, drop unknown tools, infer intent
	return a.validatePlanSteps(steps, isAuto, inferredIntent, trigger)
}

// ── Plan tool schema for native function calling mode ──────────────────────

// executiveToolSchemaTemplate is the plan meta-tool schema with a %s placeholder
// where the intent enum goes. The enum is built at call time from the
// registry so custom intent names (admin-created via the UI) show up as
// valid values to the model.
var executiveToolSchemaTemplate = `{
	"type": "object",
	"properties": {
		"answer": {
			"type": "string",
			"description": "Direct answer for trivial questions that need no tools. Only set when steps is empty."
		},
		"intent": {
			"type": "string",
			"enum": %s,
			"description": "Inferred intent level for this plan"
		},
		"steps": {
			"type": "array",
			"items": {
				"type": "object",
				"required": ["tool", "params", "depends_on", "tag"],
				"properties": {
					"type":       {"type": "string", "enum": ["tool","compute"], "description": "Node type: tool (default) or compute (LLM code generation)"},
					"tool":       {"type": "string", "description": "Tool name from the Tools list"},
					"params":     {"type": "object", "description": "Tool input params as key-value pairs. ALWAYS populate for tools with required params marked *. Example: for web_search use {\"query\": \"search terms\"}, for bash use {\"command\": \"ls -la\"}. NEVER leave empty."},
					"depends_on": {"type": "array", "items": {"type": "integer"}, "description": "Step indices that must complete first"},
					"tag":        {"type": "string", "description": "Short human-readable label"},
					"param_refs": {
						"type": "object",
						"additionalProperties": {
							"type": "object",
							"properties": {
								"step":     {"type": "integer"},
								"field":    {"type": "string"},
								"template": {"type": "string"}
							},
							"required": ["step", "field"]
						},
						"description": "Dependency injection from upstream step outputs"
					},
					"gap": {"type": "string", "description": "For tool=gap only: describes a missing capability"}
				}
			}
		}
	},
	"required": ["steps"]
}`

/*
 * executiveToolDef returns the meta-tool definition for native function calling mode.
 * desc: Defines a single "plan" tool whose input schema matches the PlanStep array format.
 *       The model "calls" this tool with the entire DAG as its argument. The intent
 *       enum is built at call time from the registry so admin-created custom intents
 *       are presented as valid values to the model.
 * return: llm.ToolDef for the plan meta-tool.
 */
func (a *Agent) executiveToolDef() llm.ToolDef {
	// Build the intent enum dynamically from the registry. If the registry
	// hasn't been loaded the enum is omitted entirely — Go has no knowledge
	// of specific intent names to fall back on.
	var names []string
	if a.intentRegistry != nil {
		names = a.intentRegistry.AllowedNames(-1)
	}
	enumJSON, _ := json.Marshal(names)
	schema := json.RawMessage(fmt.Sprintf(executiveToolSchemaTemplate, string(enumJSON)))
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "plan",
			Description: `Submit an execution plan. Example: {"steps":[{"tool":"web_search","params":{"query":"bitcoin price"},"depends_on":[],"tag":"s1"}]}. Chaining example: {"steps":[{"tool":"web_search","params":{"query":"news"},"depends_on":[],"tag":"s1"},{"tool":"web_fetch","params":{},"param_refs":{"url":{"step":0,"field":"results.0.url"}},"depends_on":[0],"tag":"s2"}]}. Trivial: {"steps":[],"answer":"Paris."}`,
			Parameters:  schema,
		},
	}
}

/*
 * executiveCallPayload is the parsed argument from a native plan() tool call.
 * desc: Matches the planToolSchema — contains optional intent and the steps array.
 */
type executiveCallPayload struct {
	Intent string     `json:"intent"`
	Answer string     `json:"answer"`
	Steps  []PlanStep `json:"steps"`
}

// parseExecutivePayload handles LLMs returning steps as a JSON string instead of an array.
func parseExecutivePayload(raw string, payload *executiveCallPayload) error {
	if err := json.Unmarshal([]byte(raw), payload); err != nil {
		// Try parsing with steps as a string (double-encoded JSON)
		var flex struct {
			Intent string          `json:"intent"`
			Answer string          `json:"answer"`
			Steps  json.RawMessage `json:"steps"`
		}
		if err2 := json.Unmarshal([]byte(raw), &flex); err2 != nil {
			return err
		}
		payload.Intent = flex.Intent
		payload.Answer = flex.Answer
		// Try unwrapping string-encoded steps
		var stepsStr string
		if json.Unmarshal(flex.Steps, &stepsStr) == nil {
			if err3 := ParseLLMJSON(stepsStr, &payload.Steps); err3 != nil {
				return fmt.Errorf("steps is a string but not valid JSON: %w", err3)
			}
			log.Printf("[dag] executive: unwrapped string-encoded steps (%d steps)", len(payload.Steps))
			return nil
		}
		return err
	}
	return nil
}

/*
 * runExecutiveNative makes a single LLM call using native function calling.
 * desc: Sends the plan meta-tool to the LLM. The model calls plan() with the
 *       entire DAG as the argument. No text parsing, no markdown fences.
 *       Falls back to text parsing if the model responds with text instead of a tool call.
 * param: ctx - context for the LLM call.
 * param: trigger - the investigation trigger.
 * return: PlanResult pointer with steps and intent, or error.
 */
func (a *Agent) runExecutiveNative(ctx context.Context, trigger Trigger, graph *Graph) (*PlanResult, error) {
	relevant := a.relevantTools(ctx, formatTrigger(trigger), trigger.Scope)
	log.Printf("[dag] executive (native) sees %d tools: %v", len(relevant), relevant)
	if len(a.skillGuidance) > 0 {
		log.Printf("[dag] executive (native) has %d guidance skills loaded", len(a.skillGuidance))
	}

	dagMode := a.cfg.DAGMode
	if trigger.DAGMode != "" {
		dagMode = trigger.DAGMode
	}
	intent := trigger.Intent().String()
	// Preflight override: same logic as structured planner.
	if trigger.Intent() == gates.IntentAuto && a.preflight != nil {
		intent = a.preflight.Intent.String()
		log.Printf("[dag] executive (native) intent from preflight: %s", intent)
	}

	executiveHistory := prefixAssistantHistory(trigger.History)

	// Include worklog so planner knows system state from previous runs.
	// Pulled through ContextGate for centralization.
	userQuery := formatTrigger(trigger)
	// Inject preflight context framing right after the query.
	if a.preflight != nil && a.preflight.Context != "" {
		userQuery += "\n\n## Context\n" + a.preflight.Context
	}
	if graph != nil && graph.Context != nil {
		gateResp, gerr := graph.Context.Get(ctx, ContextRequest{
			ReturnSources: Sources(
				Worklog(20, "all"),
			),
			MaxBudget: 4000,
		})
		if gerr != nil {
			log.Printf("[dag] executive (native) context build failed: %v", gerr)
		} else if wl := gateResp.Sources[SourceWorklog]; wl != "" {
			userQuery += "\n\n## System State (worklog)\n```\n" + wl + "\n```"
		}
	}

	sysPromptN := a.executiveSystemPrompt(ctx, graph, relevant, dagMode, intent)
	messages := BuildMessagesWithHistory(sysPromptN, userQuery, executiveHistory)

	startedN := time.Now()
	resp, err := a.llm.Complete(ctx, &llm.ChatRequest{
		Messages:    messages,
		Tools:       []llm.ToolDef{a.executiveToolDef()},
		ToolChoice:  "required",
		Temperature: a.cfg.Temperature,
		MaxTokens:   a.cfg.MaxTokens,
	})

	traceN := LLMTrace{
		AlertID:  trigger.AlertID,
		NodeID:   "executive",
		NodeType: "executive_native",
		Tag:      "plan",
		Started:  startedN,
		Input: map[string]string{
			"dag_mode": dagMode,
			"intent":   intent,
		},
		System:    sysPromptN,
		User:      userQuery,
		LatencyMS: time.Since(startedN).Milliseconds(),
	}

	if err != nil {
		traceN.Err = err.Error()
		WriteLLMTrace(traceN)
		return nil, fmt.Errorf("planner LLM call (native): %w", err)
	}

	if len(resp.Choices) == 0 {
		traceN.Err = "no choices"
		WriteLLMTrace(traceN)
		return nil, fmt.Errorf("planner LLM returned no choices")
	}

	// Capture output text or tool call args for the trace.
	if len(resp.Choices[0].Message.ToolCalls) > 0 {
		traceN.Output = resp.Choices[0].Message.ToolCalls[0].Function.Arguments
	} else {
		traceN.Output = resp.Choices[0].Message.Content
	}
	traceN.TokensIn = resp.Usage.PromptTokens
	traceN.TokensOut = resp.Usage.CompletionTokens
	WriteLLMTrace(traceN)

	choice := resp.Choices[0]

	// Check if the model called the plan tool
	if choice.FinishReason == "tool_calls" && len(choice.Message.ToolCalls) > 0 {
		tc := choice.Message.ToolCalls[0]
		if tc.Function.Name != "plan" {
			return nil, fmt.Errorf("planner called unexpected tool %q (expected plan)", tc.Function.Name)
		}

		log.Printf("[dag] executive (native) received plan() call, %d bytes: %s", len(tc.Function.Arguments), Text.TruncateLog(tc.Function.Arguments, 500))

		// Try parsing, with fixup for malformed compute steps
		raw := tc.Function.Arguments
		fixedRaw := fixComputeStepParams(raw)

		var payload executiveCallPayload
		if err := parseExecutivePayload(fixedRaw, &payload); err != nil {
			// Retry: send the error back and ask the planner to fix
			log.Printf("[dag] executive (native) plan() parse failed, retrying: %v", err)
			retryMessages := append(messages,
				llm.Message{Role: "assistant", Content: "", ToolCalls: choice.Message.ToolCalls},
				llm.Message{Role: "tool", ToolCallID: tc.ID, Name: "plan", Content: fmt.Sprintf("Error: %v. Fix the JSON and call plan() again. Remember: goal, mode, query go INSIDE params, not at the step level.", err)},
			)
			retryResp, retryErr := a.llm.Complete(ctx, &llm.ChatRequest{
				Messages:    retryMessages,
				Tools:       []llm.ToolDef{a.executiveToolDef()},
				ToolChoice:  "required",
				Temperature: 0.1,
				MaxTokens:   a.cfg.MaxTokens,
			})
			if retryErr != nil {
				return nil, fmt.Errorf("parse plan() arguments (retry failed): %w", err)
			}
			if len(retryResp.Choices) > 0 && len(retryResp.Choices[0].Message.ToolCalls) > 0 {
				retryTC := retryResp.Choices[0].Message.ToolCalls[0]
				retryFixed := fixComputeStepParams(retryTC.Function.Arguments)
				log.Printf("[dag] executive (native) retry plan() call: %s", Text.TruncateLog(retryFixed, 500))
				if retryErr2 := parseExecutivePayload(retryFixed, &payload); retryErr2 != nil {
					return nil, fmt.Errorf("parse plan() arguments after retry: %w", retryErr2)
				}
			} else {
				return nil, fmt.Errorf("parse plan() arguments: %w", err)
			}
		}

		steps := payload.Steps

		// Empty steps = trivial query, planner answered directly
		if len(steps) == 0 {
			if payload.Answer != "" {
				log.Printf("[dag] executive answered directly (no tools needed): %s", Text.TruncateLog(payload.Answer, 200))
				return nil, &ExecutiveConversationalError{Text: payload.Answer}
			}
			// Empty steps with no answer — fallback to direct LLM response
			return nil, &ExecutiveConversationalError{}
		}

		isAuto := trigger.Intent() == gates.IntentAuto

		// Infer intent from the payload name by resolving it through the
		// registry. Unknown names leave inferredIntent at 0 and the planner
		// falls back to tool-impact inference in validatePlanSteps.
		var inferredIntent gates.Intent
		if isAuto && payload.Intent != "" && a.intentRegistry != nil {
			if i, ok := a.intentRegistry.ByName(payload.Intent); ok {
				inferredIntent = gates.Intent(i.Rank)
			}
		}

		return a.validatePlanSteps(steps, isAuto, inferredIntent, trigger)
	}

	// Fallback: model returned text instead of a tool call.
	// Try parsing as JSON (some models ignore tool calling and write text).
	raw := choice.Message.Content
	log.Printf("[dag] executive (native) returned text instead of tool call: %s", Text.TruncateLog(raw, 200))

	if len(raw) > 0 && (raw[0] == '[' || raw[0] == '{') {
		isAuto := trigger.Intent() == gates.IntentAuto
		steps, inferredIntent, parseErr := a.parseExecutiveOutput(raw, isAuto)
		if parseErr == nil {
			return a.validatePlanSteps(steps, isAuto, inferredIntent, trigger)
		}
	}

	return nil, &ExecutiveConversationalError{Text: strings.TrimSpace(raw)}
}

/*
 * validatePlanSteps applies shared validation to parsed plan steps.
 * desc: Filters gaps, drops unknown tools, validates deps, breaks cycles,
 *       deduplicates, and infers intent if auto. Used by both structured and native planners.
 * param: steps - raw parsed plan steps.
 * param: isAuto - whether intent should be auto-inferred.
 * param: inferredIntent - pre-inferred intent from payload (or IntentObserve if not set).
 * param: trigger - the original trigger for scope checking.
 * return: validated PlanResult or error.
 */
func (a *Agent) validatePlanSteps(steps []PlanStep, isAuto bool, inferredIntent gates.Intent, trigger Trigger) (*PlanResult, error) {
	// Extract gaps and filter unknown tools
	var gaps []string
	valid := steps[:0]
	for _, s := range steps {
		if s.Tool == "gap" {
			if s.Gap != "" {
				gaps = append(gaps, s.Gap)
				log.Printf("[dag] executive declared gap: %s", s.Gap)
			}
			continue
		}
		if _, ok := a.registry.Get(s.Tool); ok {
			valid = append(valid, s)
		} else {
			log.Printf("[dag] executive hallucinated unknown tool %q, dropping step", s.Tool)
		}
	}
	if len(valid) == 0 && len(gaps) == 0 {
		return nil, fmt.Errorf("planner produced no valid tools (all hallucinated)")
	}
	if len(valid) == 0 && len(gaps) > 0 {
		return nil, &ExecutiveConversationalError{
			Text: "Cannot fulfill this request. Missing capabilities: " + strings.Join(gaps, "; "),
		}
	}

	result := &PlanResult{Steps: valid, Gaps: gaps, WasAuto: isAuto}
	if isAuto {
		// Use preflight intent as a floor — inference can raise but not lower.
		// Preflight sees the full query context; tool-impact inference only
		// sees resolved impacts which may be 0 for parametric tools (bash
		// with param_refs not yet populated).
		preflightFloor := gates.Intent(0)
		if a.preflight != nil && a.preflight.Intent > 0 {
			preflightFloor = a.preflight.Intent
		}
		if inferredIntent < preflightFloor {
			inferredIntent = preflightFloor
		}
		if inferredIntent == gates.Intent(0) {
			// Resolve each tool through the intent registry so custom
			// admin-pinned intents (e.g. bash → "kill" at rank 300)
			// participate. Then snap up to the smallest registered
			// intent that covers the heaviest tool.
			maxRank := 0
			for _, s := range valid {
				skill, ok := a.registry.Get(s.Tool)
				if !ok {
					continue
				}
				rank := a.intentRegistry.ResolveToolIntent(s.Tool, skill, s.Params)
				if rank > maxRank {
					maxRank = rank
				}
			}
			inferredIntent = gates.Intent(a.intentRegistry.SnapUp(maxRank))
		}
		result.InferredIntent = inferredIntent
		log.Printf("[dag] inferred intent: %s (from plan tool impacts)", inferredIntent)
	}

	return result, nil
}

/*
 * parseExecutiveOutput extracts PlanSteps from the LLM's raw text output.
 * desc: Always expects a JSON array of steps. If the LLM wraps it in an object
 *       (e.g. {"intent":"...", "steps":[...]}), extracts the array from it.
 *       Validates deps and param_refs ranges, auto-adds missing dep edges,
 *       detects and breaks cycles, and deduplicates param_ref steps.
 * param: raw - the raw LLM output string.
 * param: isAuto - true if intent should be extracted from the response.
 * return: parsed PlanStep slice, inferred intent, or error.
 */
func (a *Agent) parseExecutiveOutput(raw string, isAuto bool) ([]PlanStep, gates.Intent, error) {
	inferredIntent := gates.Intent(0) // safe default

	var steps []PlanStep

	// Primary path: parse as JSON array
	if err := ParseLLMJSON(raw, &steps); err != nil {
		// Fallback: LLM may have wrapped in an object despite being told array-only
		var out executiveOutput
		if TryParseLLMJSON(raw, &out) && len(out.Steps) > 0 {
			steps = out.Steps
			if isAuto {
				// Resolve via the registry. Unknown names leave inferredIntent
				// at 0 (the safest default) and downstream tool-impact
				// inference in validatePlanSteps takes over.
				if i, ok := a.intentRegistry.ByName(out.Intent); ok {
					inferredIntent = gates.Intent(i.Rank)
				}
			}
			log.Printf("[dag] executive returned object instead of array, extracted %d steps", len(steps))
		} else {
			return nil, inferredIntent, fmt.Errorf("invalid JSON: %w", err)
		}
	}

	if len(steps) == 0 {
		// Empty plan means no tools needed — this is a conversational query.
		// Return as conversational error so the caller can handle it.
		return nil, inferredIntent, &ExecutiveConversationalError{Text: ""}
	}

	// Debug: log parsed param_refs to diagnose injection failures
	for i, s := range steps {
		if len(s.ParamRefs) > 0 {
			for pn, ref := range s.ParamRefs {
				log.Printf("[dag] parsed step %d param_ref %s: step=%d field=%q template=%q", i, pn, ref.Step, ref.Field, ref.Template)
			}
		}
	}

	// Validate index-based deps and param_refs are in range
	for i, s := range steps {
		if s.Tool == "" {
			return nil, inferredIntent, fmt.Errorf("step %d missing tool name", i)
		}
		// Filter out invalid deps (out of range, self-reference)
		validDeps := s.DependsOn[:0]
		for _, dep := range s.DependsOn {
			if dep < 0 || dep >= len(steps) {
				log.Printf("[dag] step %d depends_on index %d out of range, skipping", i, dep)
				continue
			}
			if dep == i {
				log.Printf("[dag] step %d depends on itself, removing self-reference", i)
				continue
			}
			validDeps = append(validDeps, dep)
		}
		steps[i].DependsOn = validDeps
		// Validate param_refs and auto-add to depends_on if missing
		for paramName, ref := range s.ParamRefs {
			if ref.Step < 0 || ref.Step >= len(steps) {
				log.Printf("[dag] step %d param_ref %q references step %d out of range, skipping", i, paramName, ref.Step)
				delete(s.ParamRefs, paramName)
				continue
			}
			if ref.Step == i {
				log.Printf("[dag] step %d param_ref %q references itself, skipping", i, paramName)
				delete(s.ParamRefs, paramName)
				continue
			}
			if ref.Field == "" {
				log.Printf("[dag] step %d param_ref %q has empty field, skipping injection", i, paramName)
				delete(s.ParamRefs, paramName)
				continue
			}
			// Enforce ordering: injection source must be in depends_on so it
			// resolves before this node fires. Auto-add if the LLM forgot.
			found := false
			for _, dep := range s.DependsOn {
				if dep == ref.Step {
					found = true
					break
				}
			}
			if !found {
				s.DependsOn = append(s.DependsOn, ref.Step)
				steps[i] = s
			}
		}
	}

	// Cycle detection — a DAG must be acyclic. Detect and break any cycles.
	// Uses topological sort; steps involved in cycles have their offending deps removed.
	if hasCycle, fixed := breakCycles(steps); hasCycle {
		log.Printf("[dag] cycle detected in plan, removed offending dependencies")
		steps = fixed
	}

	// Dedup: drop steps that fetch the same URL via identical param_refs.
	// The planner sometimes creates two fetch phases for the same search results
	// with different focus params — one fetch with a broad focus is sufficient.
	steps = deduplicateParamRefSteps(steps)

	return steps, inferredIntent, nil
}

/*
 * breakCycles checks for cycles in the step dependency graph.
 * desc: Uses DFS with visit states (unvisited/visiting/visited). If a back
 *       edge is found, the offending dependency is removed to break the cycle.
 * param: steps - the plan steps to check.
 * return: true if any cycles were found, and the fixed steps.
 */
func breakCycles(steps []PlanStep) (bool, []PlanStep) {
	n := len(steps)
	// States: 0=unvisited, 1=visiting (in current DFS path), 2=visited
	state := make([]int, n)
	hasCycle := false

	var dfs func(i int) bool
	dfs = func(i int) bool {
		state[i] = 1
		newDeps := steps[i].DependsOn[:0]
		for _, dep := range steps[i].DependsOn {
			if dep < 0 || dep >= n {
				continue
			}
			if state[dep] == 1 {
				// Back edge — this is a cycle. Drop this dependency.
				log.Printf("[dag] breaking cycle: step %d → step %d", i, dep)
				hasCycle = true
				continue
			}
			if state[dep] == 0 {
				if dfs(dep) {
					// Cycle found deeper — deps already cleaned
				}
			}
			newDeps = append(newDeps, dep)
		}
		steps[i].DependsOn = newDeps
		state[i] = 2
		return hasCycle
	}

	for i := 0; i < n; i++ {
		if state[i] == 0 {
			dfs(i)
		}
	}
	return hasCycle, steps
}

/*
 * deduplicateParamRefSteps removes steps that have identical tool + param_ref
 * source (same step + same field).
 * desc: Catches the common case where the planner creates two fetch phases for
 *       the same search results with different focus params. Merges focus params
 *       into the first occurrence and drops the duplicate.
 * param: steps - the plan steps to deduplicate.
 * return: deduplicated steps with remapped depends_on and param_ref indices.
 */
func deduplicateParamRefSteps(steps []PlanStep) []PlanStep {
	type refKey struct {
		tool  string
		step  int
		field string
	}

	seen := make(map[refKey]int) // refKey → first step index
	dropSet := make(map[int]bool)

	for i, s := range steps {
		if len(s.ParamRefs) == 0 {
			continue
		}
		// Use the first param_ref as the dedup key (usually "url")
		for _, ref := range s.ParamRefs {
			key := refKey{tool: s.Tool, step: ref.Step, field: ref.Field}
			if firstIdx, exists := seen[key]; exists {
				// Duplicate — merge focus params if possible, drop this step
				if firstFocus, ok := steps[firstIdx].Params["focus"].(string); ok {
					if dupFocus, ok2 := s.Params["focus"].(string); ok2 && dupFocus != firstFocus {
						// Merge focuses: "pricing, features" + "capabilities, extensibility"
						steps[firstIdx].Params["focus"] = firstFocus + ", " + dupFocus
					}
				}
				dropSet[i] = true
				log.Printf("[dag] dedup: step %d (%s) duplicates step %d (same %s.%s), merging", i, s.Tag, firstIdx, s.Tool, ref.Field)
			} else {
				seen[key] = i
			}
			break // only check first param_ref
		}
	}

	if len(dropSet) == 0 {
		return steps
	}

	// Rebuild steps without dropped entries, remapping depends_on indices
	indexMap := make(map[int]int) // old index → new index
	var result []PlanStep
	for i, s := range steps {
		if dropSet[i] {
			continue
		}
		indexMap[i] = len(result)
		result = append(result, s)
	}

	// Remap depends_on and param_ref step indices
	for i := range result {
		newDeps := result[i].DependsOn[:0]
		for _, dep := range result[i].DependsOn {
			if newIdx, ok := indexMap[dep]; ok {
				newDeps = append(newDeps, newIdx)
			}
			// If dep was dropped, skip it (its work is merged into the kept step)
		}
		result[i].DependsOn = newDeps

		for pn, ref := range result[i].ParamRefs {
			if newIdx, ok := indexMap[ref.Step]; ok {
				ref.Step = newIdx
				result[i].ParamRefs[pn] = ref
			}
		}
	}

	log.Printf("[dag] dedup removed %d duplicate steps (%d → %d)", len(dropSet), len(steps), len(result))
	return result
}

/*
 * planStepsToNodes converts parsed plan steps into graph nodes.
 * desc: Two-pass: first create all nodes (collecting IDs), then resolve index
 *       deps and param_refs to real node IDs. Filters duplicate tool+params
 *       against already-executed nodes (for replan grafts). Optionally injects
 *       reflection nodes between depth waves (reflect mode only).
 * param: steps - the parsed plan steps.
 * param: graph - the investigation graph.
 * param: budget - the execution budget.
 * param: registry - tool registry for source tagging and schema validation (optional).
 * param: dagMode - optional DAG mode override for reflection injection.
 * return: slice of created Node pointers, or error.
 */
func planStepsToNodes(steps []PlanStep, graph *Graph, budget *Budget, registry *tools.Registry, dagMode ...string) ([]*Node, error) {
	// Pass 1: create nodes and collect their graph IDs
	nodeIDs := make([]string, len(steps))
	nodes := make([]*Node, len(steps))

	for i, s := range steps {

		// At plan time, only check total node count — not per-tool wave limits.
		// Per-tool limits are for execution batching, not planning.
		// Pass "" for tool to skip wave counter; pass false for isLLM (tool node).
		if !budget.TrySpawnNode("", false) {
			log.Printf("[dag] budget exhausted at step %d, truncating plan", i)
			nodes = nodes[:i]
			break
		}

		// Validate tool exists — reject hallucinated tool names at graft time
		// instead of failing at execution time with "unknown tool"
		if s.Tool != "compute" && s.Tool != "" && registry != nil {
			if _, ok := registry.Get(s.Tool); !ok {
				log.Printf("[dag] dropping step %q — unknown tool %q (hallucinated)", s.Tag, s.Tool)
				continue
			}
		}

		nodeType := NodeTool
		if s.Type == "compute" || s.Tool == "compute" {
			nodeType = NodeCompute
		}
		n := &Node{
			Type:     nodeType,
			ToolName: s.Tool,
			Params:   s.Params,
			Tag:      s.Tag,
		}
		// Tag the node with its tool source for frontend display
		if registry != nil {
			n.Source = registry.GetSource(s.Tool)
		}
		id := graph.AddNode(n)
		nodeIDs[i] = id
		nodes[i] = n
	}

	// Pass 2: resolve index-based deps and param_refs to real node IDs
	for i, s := range steps {
		if i >= len(nodes) || nodes[i] == nil {
			break // truncated by budget
		}
		for _, depIdx := range s.DependsOn {
			if depIdx < len(nodeIDs) && nodeIDs[depIdx] != "" {
				nodes[i].DependsOn = append(nodes[i].DependsOn, nodeIDs[depIdx])
			}
		}

		// Resolve param_refs step indices → node IDs (dependency injection).
		// parseExecutiveOutput already validated step ranges; skip invalid as safety net.
		if len(s.ParamRefs) > 0 {
			resolved := make(map[string]ResolvedInjection, len(s.ParamRefs))
			for paramName, ref := range s.ParamRefs {
				if ref.Step < 0 || ref.Step >= len(nodeIDs) || nodeIDs[ref.Step] == "" {
					log.Printf("[dag] param_ref %q on step %d references invalid step %d, skipping", paramName, i, ref.Step)
					continue
				}
				resolved[paramName] = ResolvedInjection{
					NodeID:   nodeIDs[ref.Step],
					Field:    ref.Field,
					Template: ref.Template,
				}
				// Ensure the referenced step is in DependsOn (safety net)
				refNodeID := nodeIDs[ref.Step]
				hasDep := false
				for _, d := range nodes[i].DependsOn {
					if d == refNodeID {
						hasDep = true
						break
					}
				}
				if !hasDep {
					nodes[i].DependsOn = append(nodes[i].DependsOn, refNodeID)
				}
			}
			nodes[i].ParamRefs = resolved
			for pn, ri := range resolved {
				if ri.Template != "" {
					log.Printf("[dag] param_ref %s.%s ← %s.%s (template: %s)", nodes[i].ID, pn, ri.NodeID, ri.Field, ri.Template)
				} else {
					log.Printf("[dag] param_ref %s.%s ← %s.%s", nodes[i].ID, pn, ri.NodeID, ri.Field)
				}
			}
			// Validate field paths against upstream output schemas.
			// Warnings only — planning is permissive. If the field is truly
			// missing, resolveInjections will fail fast at execution time.
			if registry != nil {
				for pn, ref := range s.ParamRefs {
					upstreamTool := steps[ref.Step].Tool
					if skill, ok := registry.Get(upstreamTool); ok {
						outSchema := tools.GetOutputSchema(skill)
						if outSchema == nil {
							log.Printf("[dag] warning: param_ref %s.%s references %s which has no output schema", nodes[i].ID, pn, upstreamTool)
						} else if !fieldExistsInSchema(outSchema, ref.Field) {
							log.Printf("[dag] warning: param_ref %s.%s references field %q not in %s output schema", nodes[i].ID, pn, ref.Field, upstreamTool)
						}
					}
				}
			}
		}
	}

	// Wave reflections removed — the scheduler handles reflection timing.
	// Injecting reflections at plan time caused cascading debugger spawns
	// when early waves failed and all reflection nodes became ready at once.

	return nodes, nil
}

/*
 * fieldExistsInSchema checks if a dot-path field exists in a JSON Schema's properties.
 * desc: Used to validate param_refs field paths against declared output schemas
 *       at plan time. Supports nested objects and array items traversal.
 * param: schemaJSON - the raw JSON Schema bytes.
 * param: fieldPath - dot-separated field path to validate.
 * return: true if the field path exists in the schema.
 */
func fieldExistsInSchema(schemaJSON json.RawMessage, fieldPath string) bool {
	var schema map[string]any
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return false
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return false
	}
	parts := strings.Split(fieldPath, ".")
	current := props
	for i, part := range parts {
		// Numeric index — skip this segment (we're indexing into an array
		// whose items properties are already loaded in `current`).
		if isNumericPathSegment(part) {
			continue
		}
		prop, exists := current[part]
		if !exists {
			return false
		}
		if i == len(parts)-1 {
			return true
		}
		propObj, ok := prop.(map[string]any)
		if !ok {
			return false
		}
		// Array type: descend into items.properties
		if propObj["type"] == "array" {
			items, ok := propObj["items"].(map[string]any)
			if !ok {
				return false
			}
			nested, ok := items["properties"].(map[string]any)
			if !ok {
				return false
			}
			current = nested
			continue
		}
		nested, ok := propObj["properties"].(map[string]any)
		if !ok {
			return false
		}
		current = nested
	}
	return true
}

/*
 * isNumericPathSegment returns true if the string contains only digits.
 * desc: Used by fieldExistsInSchema to detect array index segments in dot-paths.
 * param: s - the string to check.
 * return: true if s is non-empty and all characters are digits.
 */
func isNumericPathSegment(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
