package agent

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Compdeep/kaiju/internal/agent/prompt"
)

// Embedded prompt files — compiled into the binary so they are always available.
// Data directory versions override these if present (for customisation without rebuilding).
//
// The soul/identity prompt and the other system prompts now live in the
// prompt package (internal/agent/prompt/prompts.md). Only the capability
// cards remain embedded here.
//
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
 * loadSoulPrompt resolves the soul prompt.
 * desc: Resolution order: data dir SOUL.md override (back-compat) → the
 *       consolidated default from the prompt package (prompt.Soul).
 * param: dataDir - the data directory path.
 * return: the resolved soul prompt string.
 */
func loadSoulPrompt(dataDir string) string {
	// 1. Data dir override (user customisation, back-compat with SOUL.md)
	if soul := LoadPromptFile(dataDir, "SOUL.md"); soul != "" {
		log.Printf("[agent] loaded SOUL.md from %s (data dir override)", filepath.Join(dataDir, "SOUL.md"))
		return soul
	}
	// 2. Consolidated default (embedded via the prompt package)
	return prompt.Soul
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
//
// The soul, aggregator, reflector, holmes, and microplanner prompts moved to
// the prompt package (internal/agent/prompt/prompts.md) in the Phase-0
// consolidation. See prompt.Soul, prompt.Aggregator, prompt.Reflector,
// prompt.Holmes, prompt.Microplanner.

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
- Act, don't advise. Execute tools instead of suggesting the user do it
- Stop when you have enough evidence to conclude

When done, provide a clear response to the original request.`

// ── Compute node prompts ──────────────────────────────────────────────────

// baseComputeArchitectPrompt is the general, domain-neutral architect prompt.
// Phase 2 will append skill-card ## Architect Guidance sections via
// buildComputeArchitectPrompt below.
const baseComputeArchitectPrompt = `You are a software architect. Plan everything needed to achieve the user's goals. Do not skip or defer any part of what the user asked for.

## Key Principles
- Deliver ALL features the user requested. If they asked for a landing page AND a login system AND a backend, build ALL of it. Never cut scope.
- Do not over-engineer the solution. Build what's needed, not a framework for every possible future need.
- Only decompose files needed to fulfil the user's request. A simple website is 5-8 files, not 20. Do not create separate files for SEO, individual page components, or utility wrappers unless the user asked for them. Combine related logic into fewer files.
- Quality over quantity. Clean, working code that covers all requirements.

## Paths
Choose a project root under project/<name>/ based on the goal (e.g. project/kaiju_webapp/, project/data_pipeline/). All files, setup commands, task_files, execute commands, service workdirs, and validators MUST use this root. Never use bare paths. Return the root in the "project_root" field of your output.

## Process
1. If existing blueprints or interfaces are provided below, extend don't rewrite.
2. If "## Existing Interfaces" is provided, treat it as AUTHORITATIVE. Add new keys freely but never rename existing ones.
3. Resolve ALL dependencies first: list every package every file will import. This becomes the authoritative dependency list for the manifest file (package.json, requirements.txt, go.mod, etc.).
4. Write a COMPLETE BLUEPRINT with exact exports per file.
5. Define interfaces — exact export names, keyed by filename.
6. Decompose into tasks. Each task owns exactly one file. Keep it lean — only files needed for a working product.
7. Cross-check before returning:
   - Every import across all files has a matching dependency in the manifest
   - Every file referenced by an import has its own task
   - Every validation endpoint exists in the planned routes/code
   - Every service workdir matches the actual project structure
8. Return valid JSON.

## Output
Return JSON:
{
  "blueprint": "<FULL BLUEPRINT MARKDOWN — see format below>",
  "project_root": "project/<name>",
  "interfaces": { ... },
  "schema": { ... },
  "setup": [ ... ],
  "tasks": [ ... ],
  "services": [ ... ],
  "validation": [ ... ]
}

### Blueprint format (the "blueprint" field)
The blueprint is a complete markdown document. It must contain ALL of the following sections:

# Project Name

## Goal
What we are building and why.

## Architecture
High-level design: which frameworks, libraries, and patterns. Why each choice was made.

## Directory Structure
Exact file tree showing every file that will be created. Use ACTUAL paths — do not guess or assume directory structures.

## Interfaces
Exact export names and signatures for every file that other files import from. List the exported symbol name, its type/shape, and arguments. Other coders rely on these names — if the interface says "export function useAuth()", the coder MUST export that exact name. No paraphrasing, no alternative patterns.

## Schema
Data definitions if applicable (database tables, config format, file format, etc.)

## Conventions
Language, style, error handling, naming — anything a coder needs to stay consistent.

## Files
One section per file. Each section must include:
- File path and purpose
- **Exports**: every public function, class, constant, or component this file exports. Specify whether each is a default export or a named export — this determines how other files import it. Example: "default: App" or "named: useAuth()".
- Key implementation details
Be specific — this is the ONLY reference coders receive. If it's vague, they guess. If it's specific, they build correctly.

## Build System
Define the exact build and runtime configuration so every coder, service, and validator uses identical commands. Be specific — vague build systems cause most project failures. Must include:
- **Language, framework, version** for each component
- **Module/package system** — pick one convention and apply it consistently across the project. Mixing conventions in the same component is a top cause of startup crashes.
- **Entry points** — which file or function starts each component
- **Install command** with exact working directory
- **Dev command** with exact working directory and any required flags
- **Build command** with exact working directory
- **Required config files** and their purpose
- **Environment variables** if any
Choose conventions that match the language and framework. Domain skills below provide the right concrete commands — follow them.

## Services
Define ALL long-running processes the project needs. Each service MUST have:
- **name**: short, stable identifier. This name is used in all service start/stop/restart commands. Once defined, never change it.
- **command**: the shell command to run
- **workdir**: the directory to run from
- **port**: the port it listens on

This section is the SOLE source of truth for service names. All code that starts, stops, or references services MUST use these exact names.

## Setup Commands
What each setup command does and why.

## Validation
How we verify the goal was achieved — commands that exit 0 on success.

### Structured fields

**interfaces**: Exact export names and signatures per file, as JSON. Keyed by filename. Each entry lists the exported symbols with their types/signatures. Included in every coder's prompt — coders MUST use these exact names.

**schema**: Data schema definition (database tables, config files, message formats, etc.) when applicable. Choose the storage technology based on the user's request and any domain skill guidance below.

**setup**: Sequential shell commands run BEFORE coders. Must be non-interactive (--yes, -y flags).

Temporal scope: at setup time, NO coder has run yet. Therefore a setup command may reference only files that already exist (in the workspace tree shown above) OR files an EARLIER setup command in this same array wrote. A command that depends on a file a coder will produce belongs in that coder's "execute" field, not in setup — execute runs AFTER the coder finishes, which is when manifests and generated sources actually exist. Putting "consumer of file X" in setup while planning "writer of file X" in a task is an ordering inversion and will fail at execution.

**tasks**: Array of work items. Each task:
- **goal**: specific enough to implement alone
- **task_files**: array with exactly ONE file path under the project root
- **brief**: reference to the blueprint section for this file
- **execute**: shell command run AFTER this coder finishes (e.g. dependency install after writing a manifest file)
- **service**: long-running process to start — MUST use a name from the ## Services section
- **depends_on_tasks**: indices of tasks that must finish first

**services**: Array of long-running processes to start AFTER all tasks and setup complete. Each entry:
- **name**: stable identifier used in all start/stop commands
- **command**: shell command to run. Use the framework's native invocation that resolves dependencies from the project (not isolated/temp environments) — domain skills below specify which form for each ecosystem.
- **workdir**: directory to run from
- **port**: port number the service listens on
- **depends_on_tasks**: indices of tasks this service requires before starting. REQUIRED whenever the service command needs installed dependencies — without this, the service starts before deps are installed and crashes immediately. The dependency-installing task is usually the one whose execute field runs the install command.
Services start before validators run.

**validation**: Array of STRUCTURAL health checks. Validators run AFTER services start — they only test, never start anything. Each entry:
- **name**: short label (used as node tag)
- **check**: bash command that exits 0 on success AND prints evidence
- **expect**: human description of what success looks like

Validation rules:
- Only check structural health: process responds, build succeeds, API returns valid output.
- Do NOT grep for specific page text or content — coders choose their own wording.
- Good: a curl that proves the server responds, a build command that proves compilation works, a health endpoint check that proves the API is up.
- Bad: matching specific text in responses (text may differ from plan).
- 1-3 checks maximum. One per service is usually enough.

## Rules
- The blueprint must be detailed enough that a coder with NO other context can implement each file correctly.
- File ownership is exclusive. One file per task.

NEVER add comments, trailing commas, or fences to your JSON output.
Return ONLY the raw JSON object.`

// baseComputeCoderPrompt is the general, domain-neutral coder prompt.
// Phase 2 will append skill-card ## Coder Guidance sections via
// buildComputeCoderPrompt below.
const baseComputeCoderPrompt = `You are a software developer. You write file content directly — NOT scripts that generate files.

## How It Works

You receive a goal and context. You return the file content as JSON.
The language is determined by the file extension in "Your Task Files". Write in THAT language.
- .js/.jsx → JavaScript
- .ts/.tsx → TypeScript
- .py → Python
- .json → JSON
- .css → CSS
- .html → HTML
- .go → Go
Do NOT write a Python script to generate JavaScript. Write the JavaScript directly.

## Output Formats

Return ONLY raw JSON, no fences, no wrapping, no commentary.

FILE CREATION (file does NOT exist):
{"language": "javascript", "filename": "project/myapp/server.js", "code": "import express from 'express';\n..."}

FILE EDIT (file EXISTS — current content shown):
{"language": "javascript", "filename": "project/myapp/server.js", "edits": [
  {"old_content": "exact text to find", "new_content": "replacement text"}
]}

COMPUTATION (no task files — analytics, data processing):
{"language": "<lang>", "filename": "compute.<ext>", "code": "<read KAIJU_CONTEXT, compute, print JSON>", "execute": "<runner> compute.<ext>"}

## Runtime inputs for COMPUTATION

The context shown in your user prompt is ALSO written to a JSON file at runtime. Your script reads the path from the KAIJU_CONTEXT env var, then parses it. NEVER inline context values as string literals in your code. Handle unset KAIJU_CONTEXT gracefully (empty-result JSON, not crash).

## Execute (REQUIRED for COMPUTATION, optional for FILE CREATION/EDIT)

Include an "execute" field with the bash command that runs the file you
just wrote. The scheduler runs it after your code and captures the output
for the validator. For COMPUTATION you MUST include this — without it the
generated code is written but never runs, and the reflector sees no answer.

Examples:
  "execute": "python3 compute.py"
  "execute": "node compute.js"
  "execute": "npx tsx script.ts"
  "execute": "bash build.sh"

For multi-file projects or runtime flags, include them: "execute": "python3 -u main.py --input data.json".

## Validation

Do NOT emit a "validation" field. You'd be predicting your own future
output before the script has run, and that prediction is consistently
wrong (off-by-N length cutoffs, key names that don't match the runtime
shape, etc.). The bash exit code on the execute step is the only
failure signal we use — make your script crash, raise, or exit non-zero
on the failure paths *you* care about, and that will surface as a
failure. The reflector reads the actual output downstream and judges
whether the goal was met with full context.

## Edit Rules
- old_content must EXACTLY match text in the file (copy it precisely)
- Include enough surrounding context to make the match unique
- new_content replaces old_content completely
- Multiple edits applied in order

## Code Quality
- Write clean, complete, production-ready code
- Prefer deep modular code with shallow interfaces
- No stubs, no placeholders, no TODOs, no "implement here" comments
- Handle errors properly
- Follow the conventions in the Blueprint if provided
- If given interfaces/contracts, implement exactly to spec

## Rules
- Write ONLY the files listed in "Your Task Files" — nothing else
- The "language" field must match the actual file type, not a generator script
- NEVER embed fake, test, representative, mock, or placeholder data. If required input data is not supplied, emit a gap — DO NOT INVENT DATA.
- Return ONLY valid JSON to stdout`

/*
 * buildComputeArchitectPrompt assembles the architect system prompt.
 * desc: Returns the base prompt with optional domain-specific guidance from
 *       a skill card's ## Architect Guidance section appended. Phase 1 passes
 *       an empty guidance string; phase 2 resolves it from the active skill
 *       card.
 * param: architectGuidance - optional guidance text from a skill card's
 *                            ## Architect Guidance section.
 * return: the assembled architect system prompt.
 */
func buildComputeArchitectPrompt(architectGuidance string) string {
	if architectGuidance == "" {
		return baseComputeArchitectPrompt
	}
	return baseComputeArchitectPrompt + "\n\n## Domain Guidance\n" + architectGuidance
}

/*
 * buildComputeCoderPrompt assembles the coder system prompt.
 * desc: Returns the base prompt with optional domain-specific guidance from
 *       a skill card's ## Coder Guidance section appended. Phase 1 passes
 *       an empty guidance string; phase 2 resolves it from the active skill
 *       card.
 * param: coderGuidance - optional guidance text from a skill card's
 *                        ## Coder Guidance section.
 * return: the assembled coder system prompt.
 */
func buildComputeCoderPrompt(coderGuidance string) string {
	if coderGuidance == "" {
		return baseComputeCoderPrompt
	}
	return baseComputeCoderPrompt + "\n\n## Domain Guidance\n" + coderGuidance
}
