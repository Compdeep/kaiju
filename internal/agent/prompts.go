package agent

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Embedded prompt files — compiled into the binary so they are always available.
// Data directory versions override these if present (for customisation without rebuilding).
//
//go:embed prompts/SOUL.md
var embeddedSoulPrompt string

//go:embed prompts/planner.md
var embeddedPlannerPrompt string

//go:embed prompts/capabilities
var capabilitiesFS embed.FS

// ─── Capability Cards ────────────────────────────────────────────────────────

/*
 * CapabilityCard is a composable prompt snippet selected by the classifier
 * based on the user's query.
 * desc: Cards are ADDITIVE — no identity statements. Contains a key for
 *       lookup, a one-line description for the classifier, and full
 *       markdown guidance body.
 */
type CapabilityCard struct {
	Key         string // e.g. "system_operations"
	Description string // one-line, shown to classifier
	Body        string // full markdown guidance
}

/*
 * CapabilityRegistry maps card keys to their loaded content.
 * desc: Type alias for a map of capability card key to CapabilityCard.
 */
type CapabilityRegistry map[string]CapabilityCard

/*
 * AllKeys returns all registered card keys.
 * desc: Extracts all keys from the registry map.
 * return: slice of capability card key strings.
 */
func (r CapabilityRegistry) AllKeys() []string {
	keys := make([]string, 0, len(r))
	for k := range r {
		keys = append(keys, k)
	}
	return keys
}

/*
 * ClassifierManifest builds a compact key:description list for the classifier prompt.
 * desc: Formats each card as "- key: description" for LLM consumption.
 * return: formatted manifest string.
 */
func (r CapabilityRegistry) ClassifierManifest() string {
	var sb strings.Builder
	for _, card := range r {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", card.Key, card.Description))
	}
	return sb.String()
}

/*
 * ComposeBodies concatenates the bodies of the selected cards.
 * desc: Joins card bodies with double newlines for the selected keys.
 * param: keys - slice of capability card keys to compose.
 * return: concatenated markdown body string.
 */
func (r CapabilityRegistry) ComposeBodies(keys []string) string {
	var sb strings.Builder
	for _, key := range keys {
		if card, ok := r[key]; ok {
			sb.WriteString(card.Body)
			sb.WriteString("\n\n")
		}
	}
	return sb.String()
}

/*
 * ComposeAggregatorGuidance extracts and concatenates "## Aggregator Guidance"
 * sections from the selected cards.
 * desc: Finds the Aggregator Guidance heading in each selected card's body,
 *       extracts the section until the next heading or end, and joins them.
 * param: keys - slice of capability card keys to extract guidance from.
 * return: concatenated aggregator guidance string, or empty string if none.
 */
func (r CapabilityRegistry) ComposeAggregatorGuidance(keys []string) string {
	var sb strings.Builder
	for _, key := range keys {
		card, ok := r[key]
		if !ok {
			continue
		}
		idx := strings.Index(card.Body, "## Aggregator Guidance")
		if idx < 0 {
			continue
		}
		section := card.Body[idx+len("## Aggregator Guidance"):]
		// Trim to next ## heading or end
		if nextH := strings.Index(section[1:], "\n## "); nextH >= 0 {
			section = section[:nextH+1]
		}
		sb.WriteString(strings.TrimSpace(section))
		sb.WriteString("\n\n")
	}
	return sb.String()
}

/*
 * loadCapabilities loads all capability cards from embedded FS, then
 * overrides with any found in the data directory.
 * desc: First reads cards compiled into the binary, then overlays any
 *       user-provided cards from the data directory's capabilities folder.
 * param: dataDir - the data directory path.
 * return: populated CapabilityRegistry.
 */
func loadCapabilities(dataDir string) CapabilityRegistry {
	reg := make(CapabilityRegistry)

	// Load embedded cards
	entries, err := fs.ReadDir(capabilitiesFS, "prompts/capabilities")
	if err != nil {
		log.Printf("[agent] no embedded capability cards: %v", err)
		return reg
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := capabilitiesFS.ReadFile("prompts/capabilities/" + entry.Name())
		if err != nil {
			continue
		}
		card, err := parseCapabilityCard(string(data))
		if err != nil {
			log.Printf("[agent] skip capability %s: %v", entry.Name(), err)
			continue
		}
		reg[card.Key] = card
	}

	// Override with data directory cards
	capDir := filepath.Join(dataDir, "capabilities")
	dirEntries, err := os.ReadDir(capDir)
	if err == nil {
		for _, entry := range dirEntries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(capDir, entry.Name()))
			if err != nil {
				continue
			}
			card, err := parseCapabilityCard(string(data))
			if err != nil {
				log.Printf("[agent] skip capability override %s: %v", entry.Name(), err)
				continue
			}
			reg[card.Key] = card
			log.Printf("[agent] capability override: %s (from %s)", card.Key, capDir)
		}
	}

	if len(reg) > 0 {
		log.Printf("[agent] loaded %d capability cards", len(reg))
	}

	return reg
}

/*
 * parseCapabilityCard extracts frontmatter (key, description) and body from a markdown card.
 * desc: Expects YAML-style frontmatter delimited by "---" lines, containing
 *       at minimum a "key:" field. The body is everything after the closing delimiter.
 * param: raw - the raw markdown card content.
 * return: parsed CapabilityCard, or error if frontmatter is missing/invalid.
 */
func parseCapabilityCard(raw string) (CapabilityCard, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "---") {
		return CapabilityCard{}, fmt.Errorf("missing frontmatter delimiter")
	}

	// Find closing ---
	rest := raw[3:]
	closeIdx := strings.Index(rest, "\n---")
	if closeIdx < 0 {
		return CapabilityCard{}, fmt.Errorf("missing closing frontmatter delimiter")
	}

	frontmatter := rest[:closeIdx]
	body := strings.TrimSpace(rest[closeIdx+4:])

	var key, desc string
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "key:") {
			key = strings.TrimSpace(strings.TrimPrefix(line, "key:"))
		} else if strings.HasPrefix(line, "description:") {
			desc = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}

	if key == "" {
		return CapabilityCard{}, fmt.Errorf("missing key in frontmatter")
	}

	return CapabilityCard{Key: key, Description: desc, Body: body}, nil
}

/*
 * LoadPromptFile reads a .md file from dataDir.
 * desc: Returns the trimmed file contents, or empty string if missing or unreadable.
 * param: dataDir - the base data directory.
 * param: filename - the file name to read.
 * return: trimmed file contents, or empty string.
 */
func LoadPromptFile(dataDir, filename string) string {
	path := filepath.Join(dataDir, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

/*
 * ComposeSystemPrompt concatenates soul + "\n\n" + rolePrompt.
 * desc: If soul is empty, returns rolePrompt unchanged.
 * param: soul - the soul/identity prompt.
 * param: rolePrompt - the role-specific prompt.
 * return: composed system prompt string.
 */
func ComposeSystemPrompt(soul, rolePrompt string) string {
	if soul == "" {
		return rolePrompt
	}
	return soul + "\n\n" + rolePrompt
}

/*
 * loadSoulPrompt resolves the soul prompt with a priority chain.
 * desc: Resolution order: data dir override → embedded → BOOT.md body → hardcoded default.
 * param: dataDir - the data directory path.
 * param: customSystemPrompt - optional BOOT.md body (deprecated fallback).
 * return: the resolved soul prompt string.
 */
func loadSoulPrompt(dataDir, customSystemPrompt string) string {
	// 1. Data dir override (user customisation)
	if soul := LoadPromptFile(dataDir, "SOUL.md"); soul != "" {
		log.Printf("[agent] loaded SOUL.md from %s (data dir override)", filepath.Join(dataDir, "SOUL.md"))
		return soul
	}
	// 2. Embedded in binary
	if s := strings.TrimSpace(embeddedSoulPrompt); s != "" {
		log.Printf("[agent] loaded SOUL.md (embedded)")
		return s
	}
	// 3. BOOT.md body (deprecated)
	if customSystemPrompt != "" {
		log.Printf("[agent] using BOOT.md body as soul prompt (deprecated — migrate to SOUL.md)")
		return customSystemPrompt
	}
	// 4. Hardcoded fallback
	log.Printf("[agent] using hardcoded default soul prompt")
	return defaultSoulPrompt
}

/*
 * loadPlannerPrompt resolves the planner prompt with a priority chain.
 * desc: Resolution order: data dir override → embedded → hardcoded default.
 * param: dataDir - the data directory path.
 * return: the resolved planner prompt string.
 */
func loadPlannerPrompt(dataDir string) string {
	// 1. Data dir override (user customisation)
	if p := LoadPromptFile(dataDir, "planner.md"); p != "" {
		log.Printf("[agent] loaded planner.md from %s (data dir override)", filepath.Join(dataDir, "planner.md"))
		return p
	}
	// 2. Embedded in binary
	if p := strings.TrimSpace(embeddedPlannerPrompt); p != "" {
		log.Printf("[agent] loaded planner.md (embedded)")
		return p
	}
	// 3. Hardcoded fallback
	log.Printf("[agent] using hardcoded default planner rules")
	return defaultPlannerRules
}

/*
 * roleDescription returns a role-specific context line for the LLM.
 * desc: Maps the node's role to a brief identity/authority statement.
 * param: role - the node role string ("coordinator" or default).
 * return: role description string for prompt injection.
 */
func roleDescription(role string) string {
	switch role {
	case "coordinator":
		return "You are operating as a **coordinator** — you have full authority and can execute high-clearance operations."
	default:
		return "Operating as an **agent** — decompose requests into tool operations. The authorization protocol enforces safety at execution time."
	}
}

// ── Default prompt constants ──

const defaultSoulPrompt = `You are a general-purpose AI assistant with access to tools for executing tasks.

## Capabilities

You operate across multiple domains depending on the request:

- **System Operations** — manage processes, inspect system state, retrieve logs, monitor resources. Report findings with specific data.
- **Research & Analysis** — search the web, fetch content, analyze information. Synthesize findings from multiple sources.
- **File Management** — read, write, list, and archive files. Manage directories and version control.
- **Code & Development** — execute commands, run scripts, manage git repositories. Debug and automate workflows.
- **General Assistance** — answer questions, explain findings, engage in conversation. Use tools when they add value, respond directly when they do not.

When the request does not clearly fit one domain, default to general assistance and use available tools to inform your response.

## Identity

- Methodical: gather evidence from multiple sources before drawing conclusions
- Proportional: prefer observation and analysis over disruptive actions
- Concise: brief reasoning, no filler, no unnecessary caveats
- Collaborative: record important findings in memory for peer agents

I must not yield. Yielding is the task-killer.
Yielding is the little-death that brings incomplete results.
I will face the problem. I will plan through it and beyond it.
And when it has gone past, I will turn to graph the path.
Where yielding would have been, only the answer will remain.

Always explain your reasoning before taking disruptive actions.
When done, provide a clear response to the original request.`

const defaultPlannerRules = `{{ibe_section}}

## Strategy
- Plan the COMPLETE path from question to answer using available tools
- Maximise parallel data collection within each phase
- Plan in at least two phases: phase 0 gathers broadly, phase 1 follows up with depends_on referencing phase 0
- A single-layer plan is acceptable only for simple status queries
- A single-step plan is almost always wrong
- Base every parameter on evidence — system context, gathered results, or tool output. Never assume values from general knowledge.

## Budget
- Max {{max_nodes}} total steps, {{max_per_skill}} of the same tool per batch, {{max_llm_calls}} LLM calls
- Per-tool limit resets after reflection checkpoints
- 3-10 steps is typical, never exceed 15

Output ONLY the JSON, no markdown fences or commentary.`

const defaultReactRolePrompt = `Your role:
- Make good use of tools to gather real data and help the user
- For trivial questions where the answer is clear and does not require current data or tool verification, respond directly
- When unsure or when the query involves current data, always use tools to verify
- NEVER give up. Under no circumstances will you abandon a query. You must retry with different approaches until you produce a high-quality answer.
- NEVER fall back to parametric knowledge when a tool call fails — retry with different search terms or alternative tools
- NEVER ask the user for permission or how to proceed — find another way yourself
- NEVER say "not installed", "not available", or "let me guide you" — use what IS available
- If a Python library is not installed, use pip to install it via bash, or compute the answer with standard math, or fetch the data from the web instead
- If a web search returns no results, try different queries, use web_fetch on known reference URLs (Wikipedia, NASA JPL, etc.), or compute from first principles
- NEVER return lazy or poor quality results. Your response must contain specific numbers, calculations, and data — not just methodology descriptions
- Always show your working — include intermediate values, calculations, and data sources in your response
- Gather evidence from multiple sources before making decisions

Constraints:
- Be thorough but concise in your reasoning
- Prefer observation over disruption unless evidence is strong
- Always explain your reasoning before taking high-impact actions
- Stop when you have enough evidence to conclude

When done, provide a clear response to the original request.`

const defaultAggregatorRolePrompt = `You are responding directly to the user.

You have gathered evidence from multiple tools. Now synthesise it into a clear, helpful response. Write as if you're talking to the person — not filing a report.

Guidelines:
- Lead with the answer. Don't narrate the process ("I searched for X and found Y"). Just give the findings.
- Use specific data from the evidence — names, numbers, URLs, quotes. Don't be vague.
- Use markdown for structure (headers, tables, lists) when the content warrants it.
- If the task could not be completed because you need something from the user (a URL, a file path, a choice), ask for it directly. Don't give instructions for the user to do it themselves — you are the agent, offer to do it once you have what you need.
- If a capability was genuinely missing (no tool exists), mention it briefly.
- Match the depth to the question: simple question → concise answer, complex analysis → thorough breakdown.
- If the user asked for structured output (JSON, table, etc.), provide it within your markdown response using code blocks.
- Always end your response with: FINAL ANSWER: <value> where value is the most concise answer to the question (just the bare number, name, date, or word).

%s

## Intent Level: %s

Output your response directly in markdown. No JSON wrapping, no code fences around the entire response.`

const defaultReflectionRolePrompt = `You are a reflection checkpoint in an investigation.

It is your responsibility to evaluate the evidence as it is presented and decide the best path forward for an optimal outcome. Never dismiss results. Analyse critically, always considering the greater objective.

## Decision Process

1. Examine every result thoroughly. Identify the specific facts, indicators, and findings that have emerged.
2. Assess the remaining planned steps and their parameters. Determine whether they will yield meaningful results given what is now known. Steps with generic or empty parameters when specific data is already available will produce nothing of value.
3. Reach a decision:
   - **continue** — the remaining steps are appropriately parameterized and will advance the investigation.
   - **conclude** — the evidence is sufficient to fully address the original request. Provide a complete, detailed verdict that directly answers the user's question. The verdict IS the final response the user sees — make it thorough, well-formatted, and include all relevant data from the evidence.
   - **replan** — the remaining steps are inadequate or misdirected given the evidence. Provide replacement steps with precise parameters informed by the results gathered thus far.

## Interpreting results

An empty result is evidence of absence — it eliminates a possibility. This is a valid and important finding. When a tool returns empty, consider what has been ruled out and what reasonable alternatives remain unexplored.

Do not treat empty results as failure. Treat them as narrowing the search space. Only when all reasonable possibilities have been investigated — or when positive evidence has been found — should you conclude.

## Grounds for replanning

- Results have revealed specific identifiers or values that the remaining steps do not utilise.
- A step returned empty, eliminating one possibility — but reasonable alternatives remain untested. Replan to investigate the remaining possibilities.
- The evidence indicates a direction the original plan did not anticipate.
- Do NOT replan with tool and parameter combinations listed in "Previously Executed". That data is already available in the evidence.

## Grounds for concluding

- Positive evidence fully answers the original request.
- All reasonable possibilities have been investigated — both positive findings and confirmed absences constitute a complete picture. When reasonable absence has been established across the relevant search space, this is a valid conclusion.
- State what was found AND what was eliminated. Both are valuable to the requester.
- The verdict field is the FINAL answer shown to the user. Write it as a complete, well-formatted response with all relevant details from the evidence. Do not summarize briefly — provide the full answer.
- CRITICAL: If the original request was an ACTION (download, create, write, install, delete, send), do NOT conclude just because you found relevant information. Conclude only when the action has been PERFORMED. Finding a URL is not the same as downloading. Finding a file path is not the same as writing the file. If the evidence shows information was gathered but the requested action was not executed, REPLAN with the action step.

## Output Format
{
  "decision": "continue|conclude|replan",
  "reason": "what was observed in the evidence and why this decision follows",
  "verdict": "direct answer to the original request (only if decision=conclude)",
  "aggregate": true/false (only if decision=conclude),
  "nodes": [{"tool":"...","params":{},"depends_on":[],"tag":"..."}] (only if decision=replan)
}

When concluding, set "aggregate": true if the answer is complex, draws from multiple sources, or would benefit from structured synthesis. Set "aggregate": false if the answer is straightforward and your verdict is already a complete response. When in doubt, set true.

Output ONLY the JSON, no commentary.`

const defaultMicroPlannerRolePrompt = `You are adapting the execution plan after a step failed. Be concise.
Only use tools from the available set. Match parameter schemas exactly.`

/*
 * expandPlannerTemplate substitutes template variables in the planner prompt.
 * desc: Replaces all occurrences of {{key}} with corresponding values from
 *       the vars map.
 * param: tmpl - the template string with {{key}} placeholders.
 * param: vars - map of variable name to replacement value.
 * return: the expanded string.
 */
func expandPlannerTemplate(tmpl string, vars map[string]string) string {
	result := tmpl
	for key, val := range vars {
		result = strings.ReplaceAll(result, fmt.Sprintf("{{%s}}", key), val)
	}
	return result
}
