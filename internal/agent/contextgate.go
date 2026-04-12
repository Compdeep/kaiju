package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/llm"
)

// ContextGate is the SINGLETON context API for an investigation. There is
// exactly ONE ContextGate per Graph, constructed in setupDAGPipeline and
// reachable as graph.Context. It dies with the graph at investigation end.
//
// It is the SINGLE ENTRY POINT for prompt context retrieval. Every LLM-calling
// node in the agent funnels its context loading through Get() so there is one
// place to inspect, gate, audit, and evolve what gets injected into prompts.
// No code outside ContextGate should read worklog/blueprint/workspace state
// directly — always go through graph.Context.Get().
//
// The gate owns:
//   - The catalog of context sources (10 built-in: blueprint, worklog,
//     node_returns, workspace_tree, service_state, history, skill_guidance,
//     workspace_deep, function_map, existing_blueprints).
//   - The optional LLM-driven curator that runs when a request includes a
//     Query string.
//   - Per-call logging and (with KAIJU_PROMPT_DEBUG=1) on-disk dumps.
//
// What the gate DOES NOT own (intentional architectural boundaries):
//   - Memory. Conversational memory (semantic / episodic / procedural facts
//     about the user) lives at the chat boundary in api.go and is managed by
//     memory.Manager. It is injected into Trigger.History on the way in and
//     persisted on the way out. The execution layer (this file, the sources,
//     and every graph node) MUST NOT query or write memory. This is a
//     security boundary against prompt-injection attacks: a malicious tool
//     output cannot cause memory writes if memory is unreachable from
//     execution-layer code.
//   - Tool-facing memory (memory_store / memory_recall / memory_search) is a
//     separate path: those tools let the LLM EXPLICITLY decide to read or
//     write memory as a deliberate action, just like file_write or bash.
//     That is allowed because it requires LLM intent, not automatic injection.
//
// Concurrency: the source registry is built once at construction and read-only
// thereafter. Get() is safe to call from multiple goroutines. The curator mutex
// serializes the (rare) LLM call so we don't fire two curators in parallel for
// the same gate.
type ContextGate struct {
	graph   *Graph
	trigger *Trigger
	agent   *Agent
	sources map[string]ContextSource
	mu      sync.Mutex // serializes curator calls (cheap, infrequent)
}

// ContextRequest declares what a caller needs.
//
// If Query is non-empty, the curator LLM runs over QuerySources and produces
// a Summary. Otherwise the curator is skipped and Summary is empty.
//
// ReturnSources are loaded in declaration order — earlier entries are higher
// priority. When the cumulative size exceeds MaxBudget the gate trims, never
// errors:
//   - Sources that fully fit are kept verbatim.
//   - The boundary source (the one that overflows) is head-truncated to fit
//     the remaining budget if at least 500 chars remain, otherwise dropped.
//   - All later sources are dropped.
//
// The gate logs a warning whenever it trims so silent context collapse is
// observable. The response is still returned successfully so callers always
// have something to work with.
type ContextRequest struct {
	Query         string       // optional intent string. If set, curator runs.
	QuerySources  []SourceSpec // sources the curator reads to build the summary
	ReturnSources []SourceSpec // sources returned verbatim, ordered by priority (first = most important)
	MaxBudget     int          // total char budget. 0 = use default (32000).
}

// SourceSpec selects one source by name with optional parameters. The Params
// shape depends on the source — see helper constructors below.
type SourceSpec struct {
	Name      string
	Params    map[string]any
	TailTrunc bool // when true, truncation keeps the END of the content (newest entries) instead of the beginning
}

// ContextResponse is what callers receive from Get().
type ContextResponse struct {
	Summary string            // curator output. Empty if Query was empty.
	Sources map[string]string // verbatim ReturnSources keyed by source name.
	// Trimmed lists source names that were truncated or dropped to fit the
	// budget. Empty when everything fit. Callers can log this to surface
	// silent context degradation.
	Trimmed []string
}

// ContextSource is implemented by each registered source. It produces a string
// of context content given the current graph, trigger, agent, and source-specific
// params. Sources should not panic on missing or malformed params — return
// empty string instead.
type ContextSource interface {
	Name() string
	Load(g *Graph, t *Trigger, a *Agent, params map[string]any) (string, error)
}

// ── Construction ────────────────────────────────────────────────────────────

// NewContextGate constructs a gate bound to a specific investigation. The gate
// is registered into the Graph immediately so subsequent code can find it via
// graph.Context.
func NewContextGate(graph *Graph, trigger *Trigger, agent *Agent) *ContextGate {
	g := &ContextGate{
		graph:   graph,
		trigger: trigger,
		agent:   agent,
		sources: make(map[string]ContextSource),
	}
	g.registerDefaultSources()
	return g
}

// registerDefaultSources installs the built-in context sources. Each is
// stateless — only the registry holds them.
//
// IMPORTANT: there is intentionally no memory source. See the constants block
// below and the architectural doc at the top of this file. Memory is a
// conversation-boundary concern, not an execution-layer concern.
func (g *ContextGate) registerDefaultSources() {
	g.sources[SourceBlueprint] = &blueprintSource{}
	g.sources[SourceWorklog] = &worklogSource{}
	g.sources[SourceNodeReturns] = &nodeReturnsSource{}
	g.sources[SourceWorkspaceTree] = &workspaceTreeSource{}
	g.sources[SourceServiceState] = &serviceStateSource{}
	g.sources[SourceHistory] = &historySource{}
	g.sources[SourceSkillGuidance] = &skillGuidanceSource{}
	g.sources[SourceWorkspaceDeep] = &workspaceDeepSource{}
	g.sources[SourceFunctionMap] = &functionMapSource{}
	g.sources[SourceExistingBlueprints] = &existingBlueprintsSource{}
}

// Source name constants. Use these in SourceSpec rather than string literals
// so a typo is a compile error, not a silent miss.
//
// IMPORTANT: there is no "memory" source by design. Memory is conversational
// state that lives at the chat boundary (api.go input → trigger.History →
// aggregator output → memory.Manager.StoreMessage). The execution layer
// (ContextGate, sources, graph nodes) MUST NOT touch memory. See the
// architectural doc block at the top of this file for the reasoning.
const (
	SourceBlueprint          = "blueprint"
	SourceWorklog            = "worklog"
	SourceNodeReturns        = "node_returns"
	SourceWorkspaceTree      = "workspace_tree"
	SourceServiceState       = "service_state"
	SourceHistory            = "history"
	SourceSkillGuidance      = "skill_guidance"
	SourceWorkspaceDeep      = "workspace_deep"
	SourceFunctionMap        = "function_map"
	SourceExistingBlueprints = "existing_blueprints"
)

// ── Per-source filter / focus convention ────────────────────────────────────
//
// Each source defines its OWN param names for filtering and focus. There is
// NO shared "Focus" field — different data shapes need different narrowing
// vocabularies, and forcing uniformity hides what each source actually
// supports. Instead, every supported param is exposed via a typed helper
// constructor that documents the intent at the call site.
//
// To learn what a source supports, look at its helper constructors below
// (e.g. Blueprint, BlueprintNamed, BlueprintSection, BlueprintSections).
// New filters always come with a new constructor — do NOT add hidden params
// only readable from string literals.
//
// Currently supported per-source filters:
//
//   blueprint:
//     - name (string)            see BlueprintNamed("path/to/file.md")
//     - section (string)         see BlueprintSection("Files")
//     - sections ([]string)      see BlueprintSections("Goal", "Build System")
//
//   worklog:
//     - lines (int)              see Worklog(20, "all")
//     - filter (string)          one of "all" | "failures" | "successes"
//
//   node_returns:
//     - filter (string)          one of "all" | "failures"
//     - types ([]string)         narrow by tool name (e.g. ["bash","compute"])
//
//   workspace_tree:
//     - max_depth (int)
//     - focus_dir (string)       restrict scan to a subdirectory
//
//   workspace_deep:
//     - max_depth (int)
//
//   service_state:
//     - status (string)          one of "running" | "crashed" | ""
//     - names ([]string)         narrow to specific services
//
//   history:
//     - turns (int)              last N conversation turns
//
//   skill_guidance:
//     - topics ([]string)        e.g. ["Coder", "Debug", "Architect"]
//
//   function_map:
//     - max_depth (int)
//     - max_bytes (int)
//
//   existing_blueprints:
//     (no params yet — returns all blueprints in the session subdir)
//
// When adding a new filter:
//   1. Add the param to the source's Load() method, reading via paramString /
//      paramInt / paramStringSlice.
//   2. Add a typed helper constructor next to the existing constructors for
//      that source.
//   3. Add a one-line entry to this list.
//   4. Add a unit test in contextgate_test.go.
//   5. If the parsing logic is reusable (e.g. markdown sections), put the
//      parser in textutil.go, NOT a new file.

// ── Get: the API ────────────────────────────────────────────────────────────

const (
	defaultMaxBudget = 32000
	// gateTruncMarker is appended to a source that was head-truncated by the
	// gate's priority-trim logic. Distinct from per-source truncation markers
	// so callers can tell where the cut happened.
	gateTruncMarker = "\n\n...(truncated by contextgate to fit budget)\n"
	// gateMinChunk is the minimum number of chars left over for a boundary
	// source to be worth truncating. Below this we drop instead of truncate.
	gateMinChunk = 500
)

// loadedSource pairs a SourceSpec with its loaded content and index position.
type loadedSource struct {
	spec    SourceSpec
	content string
	idx     int // original position in ReturnSources
}

// fairShareAllocate distributes a character budget across loaded sources.
// Each source gets an equal share of the remaining budget. Sources smaller
// than their share leave surplus for the rest. Sources larger than their
// share are truncated to fit. Returns the allocated content per source
// (same order as input) and the names of any truncated sources.
//
// Truncation direction is per-source: TailTrunc keeps the end (newest),
// default keeps the beginning (highest priority content at the top).
func fairShareAllocate(sources []loadedSource, budget int) ([]string, []string) {
	n := len(sources)
	if n == 0 {
		return nil, nil
	}

	allocated := make([]string, n)
	settled := make([]bool, n)
	var trimmed []string

	remaining := budget
	unsettled := n

	// Iterate until all sources are settled. Each pass settles at least one
	// source (any that fit within their share), so this converges in ≤ n passes.
	for unsettled > 0 {
		share := remaining / unsettled
		if share <= 0 {
			share = 0
		}
		progress := false
		for i, s := range sources {
			if settled[i] || s.content == "" {
				if !settled[i] {
					settled[i] = true
					unsettled--
					progress = true
				}
				continue
			}
			if len(s.content) <= share {
				// Fits within share — settle it, surplus goes back to pool.
				allocated[i] = s.content
				remaining -= len(s.content)
				settled[i] = true
				unsettled--
				progress = true
			}
		}
		if !progress {
			// No source fit within its share this pass. Truncate all
			// remaining sources to their share.
			for i, s := range sources {
				if settled[i] || s.content == "" {
					continue
				}
				if share < gateMinChunk {
					// Not worth truncating — drop entirely.
					allocated[i] = ""
				} else {
					cut := share - len(gateTruncMarker)
					if cut < 0 {
						cut = 0
					}
					if s.spec.TailTrunc {
						// Keep the end (newest entries).
						allocated[i] = gateTruncMarker + s.content[len(s.content)-cut:]
					} else {
						// Keep the beginning (default).
						allocated[i] = s.content[:cut] + gateTruncMarker
					}
					remaining -= len(allocated[i])
				}
				trimmed = append(trimmed, s.spec.Name)
				settled[i] = true
				unsettled--
			}
			break
		}
	}

	return allocated, trimmed
}

// Get loads the requested context. This is the only public entry point.
//
// Path 1 (no Query): loads ReturnSources, returns them verbatim. No LLM call.
// Path 2 (with Query): loads QuerySources, calls the curator LLM to produce
// a relevant summary, then loads ReturnSources verbatim. One LLM call.
//
// Errors:
//   - If a requested source name is unknown: returns error.
//   - If the curator LLM fails: returns the deterministic ReturnSources with an
//     empty Summary and logs the failure (fail-open, not fail-closed).
//
// Budget overflow is NOT an error. ReturnSources are loaded in declaration
// order; if cumulative size exceeds MaxBudget the gate trims the boundary
// source to fit (when at least gateMinChunk chars remain) and drops later
// sources entirely. The trimmed source names appear in resp.Trimmed and a
// warning is logged. This avoids the failure mode where a slightly oversized
// request returns NOTHING and the caller silently runs blind.
func (g *ContextGate) Get(ctx context.Context, req ContextRequest) (*ContextResponse, error) {
	if g == nil {
		return nil, fmt.Errorf("contextgate: nil gate")
	}
	if req.MaxBudget <= 0 {
		req.MaxBudget = defaultMaxBudget
	}

	start := time.Now()
	resp := &ContextResponse{Sources: make(map[string]string)}

	// 1. Load all ReturnSources, then fair-share allocate the budget.
	//    Each source gets an equal share; sources smaller than their share
	//    leave surplus for the rest. This prevents a single large source
	//    from starving later sources. TailTrunc sources (e.g. worklog) keep
	//    their newest entries when truncated.
	var loaded []loadedSource
	for i, spec := range req.ReturnSources {
		content, err := g.loadSource(spec)
		if err != nil {
			return nil, fmt.Errorf("contextgate: load %q: %w", spec.Name, err)
		}
		loaded = append(loaded, loadedSource{spec: spec, content: content, idx: i})
	}

	allocated, trimmedNames := fairShareAllocate(loaded, req.MaxBudget)
	used := 0
	for i, ls := range loaded {
		resp.Sources[ls.spec.Name] = allocated[i]
		used += len(allocated[i])
	}
	resp.Trimmed = trimmedNames

	if len(resp.Trimmed) > 0 {
		log.Printf("[ctx] gate: budget %d, fair-share trimmed/dropped %v (used %d of %d)",
			req.MaxBudget, resp.Trimmed, used, req.MaxBudget)
	}

	// returnTotal is the post-trim total. The curator gets the leftover budget.
	returnTotal := used

	// 2. If no Query, we're done.
	if req.Query == "" {
		g.logGet(req, resp, false, time.Since(start))
		return resp, nil
	}

	// 3. Load QuerySources for the curator.
	queryInputs := make(map[string]string)
	for _, spec := range req.QuerySources {
		content, err := g.loadSource(spec)
		if err != nil {
			log.Printf("[ctx] curator: failed to load query source %q: %v", spec.Name, err)
			continue
		}
		if content != "" {
			queryInputs[spec.Name] = content
		}
	}

	// 4. Calculate remaining budget for the curator output, then proportionally
	//    truncate query inputs if their total exceeds ~4x the remaining budget
	//    (rough rule of thumb: input ~ 4x output).
	remaining := req.MaxBudget - returnTotal
	if remaining < 1000 {
		// Not enough room for a meaningful summary. Skip curator gracefully.
		log.Printf("[ctx] curator: skipping (only %d chars remaining after returns)", remaining)
		g.logGet(req, resp, false, time.Since(start))
		return resp, nil
	}
	if len(queryInputs) > 0 {
		queryInputs = proportionalTruncate(queryInputs, remaining*4)
	}

	// 5. Run the curator. Failure is fail-open: log, return without summary.
	summary, err := g.runCurator(ctx, req.Query, queryInputs, remaining)
	if err != nil {
		log.Printf("[ctx] curator failed: %v (returning raw sources)", err)
		g.logGet(req, resp, false, time.Since(start))
		return resp, nil
	}
	resp.Summary = summary
	g.logGet(req, resp, true, time.Since(start))
	return resp, nil
}

// loadSource resolves a SourceSpec against the registry and invokes the source.
// Unknown source names are an error.
func (g *ContextGate) loadSource(spec SourceSpec) (string, error) {
	if spec.Name == "" {
		return "", fmt.Errorf("source name is empty")
	}
	src, ok := g.sources[spec.Name]
	if !ok {
		return "", fmt.Errorf("unknown source %q", spec.Name)
	}
	return src.Load(g.graph, g.trigger, g.agent, spec.Params)
}

// proportionalTruncate scales each entry down so the total fits within the
// target. Falls back to head-truncation per source. Deterministic and cheap.
func proportionalTruncate(inputs map[string]string, target int) map[string]string {
	total := 0
	for _, s := range inputs {
		total += len(s)
	}
	if total <= target || total == 0 {
		return inputs
	}
	factor := float64(target) / float64(total)
	out := make(map[string]string, len(inputs))
	for name, content := range inputs {
		newLen := int(float64(len(content)) * factor)
		if newLen >= len(content) {
			out[name] = content
			continue
		}
		if newLen < 200 {
			// Don't truncate to nothing — keep at least 200 chars or drop.
			if len(content) > 200 {
				out[name] = content[:200] + "\n...(truncated)"
			} else {
				out[name] = content
			}
			continue
		}
		out[name] = content[:newLen] + "\n...(truncated)"
	}
	return out
}

// logGet emits a one-line summary of every Get() call for observability.
func (g *ContextGate) logGet(req ContextRequest, resp *ContextResponse, curatorRan bool, dur time.Duration) {
	querySrcs := make([]string, 0, len(req.QuerySources))
	for _, s := range req.QuerySources {
		querySrcs = append(querySrcs, s.Name)
	}
	returnSrcs := make([]string, 0, len(req.ReturnSources))
	for _, s := range req.ReturnSources {
		returnSrcs = append(returnSrcs, s.Name)
	}
	returnSize := 0
	for _, c := range resp.Sources {
		returnSize += len(c)
	}
	log.Printf("[ctx] gate.Get query=%q query_sources=%v return_sources=%v budget=%d return_size=%d summary_size=%d curator=%v took=%s",
		Text.TruncateLog(req.Query, 60), querySrcs, returnSrcs,
		req.MaxBudget, returnSize, len(resp.Summary), curatorRan, dur)
	// Note: per-call dumps are now captured by WriteLLMTrace in each LLM
	// caller, which includes the gate's response inline. See debuglog.go.
}

func specNames(specs []SourceSpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

// ── Curator ─────────────────────────────────────────────────────────────────

const curatorSystemPrompt = `You are a context curator for an autonomous AI agent. A node in an execution graph needs to act on a query, and has provided source materials. Your job: write a SUMMARY containing exactly the information from those sources that bears on the query. Quote VERBATIM. Drop the rest.

## Source vocabulary

- blueprint: an architectural plan for a project. Sections may include Goal, Architecture, Directory Structure, Files, Build System, Services.
- worklog: chronological log of events from this investigation. Format: TIMESTAMP TAG ACTION DETAILS.
- node_returns: results returned by previously-executed nodes (tools, compute jobs). May include errors, command output, file paths.
- workspace_tree: a light listing of files on disk in the agent's workspace.
- workspace_deep: a deep workspace scan including small file contents and structure (architect-grade).
- function_map: discovered function declarations across the workspace, formatted as a list of signatures.
- existing_blueprints: contents of all blueprints in the session, not just the latest one.
- service_state: registry of long-running processes (servers, daemons) including name, status, port, PID.
- history: recent conversation turns between the user and the agent.
- skill_guidance: instructions from active skill cards.

## Rules

1. Quote relevant content VERBATIM. Never paraphrase error messages, file paths, line numbers, stack traces, command output, package names, or stderr/stdout text. These are diagnostic keys — paraphrasing destroys them.
2. Drop irrelevant content. Do not pad with material that doesn't bear on the query.
3. Order content by relevance to the query, not by source order.
4. If nothing is relevant, return an empty summary.
5. Never invent content. Never add commentary outside the summary.
6. Stay within the size budget. If sources exceed it, prefer the most relevant content.

## Extraction patterns

7. **Pair errors with their commands.** When a command failed, include BOTH the command and its error/stderr/stdout. Just the error without the command is half-useful.
8. **Collapse recurring errors with a count.** If the same error message (or near-identical) appears multiple times across the sources, list it ONCE with a note like "(occurred 4 times: n31, n34, n45, n47)" instead of repeating it. Recurrence is itself a signal.
9. **Surface what was tried that DIDN'T work.** If the query is about a failure and the sources show prior fix attempts (DEBUG_PLAN entries, [oneshot_retry] tags, retried bash commands), call those out explicitly so the caller doesn't repeat them.
10. **Preserve workdir + paths.** When a command fails, the working directory matters as much as the error. Include "cd <dir> && ..." prefixes verbatim.
11. **Include exact identifiers.** Module names, package names, file paths, line numbers, port numbers, PIDs, function names. The query usually mentions one of these — extract content that contains it.
12. **Drop pure-progress noise.** Lines like "added N packages", "STARTED", "OK" are noise unless they contain a clue about state change relevant to the query.

Output ONLY a JSON object: {"summary": "<verbatim relevant content>"}.
No prose, no markdown fences.`

// runCurator builds the user message and calls the executor LLM. Returns the
// extracted summary string. Failures bubble up; the caller decides how to react.
func (g *ContextGate) runCurator(ctx context.Context, query string, sources map[string]string, budget int) (string, error) {
	if g.agent == nil || g.agent.executor == nil {
		return "", fmt.Errorf("no executor LLM client available")
	}
	if len(sources) == 0 {
		return "", nil // nothing to curate
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	// Build user message
	var sb strings.Builder
	sb.WriteString("QUERY:\n")
	sb.WriteString(query)
	sb.WriteString("\n\n")
	sb.WriteString(fmt.Sprintf("SIZE BUDGET: %d characters\n\n", budget))
	sb.WriteString("SOURCES:\n\n")
	// Sort source names for deterministic output
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sb.WriteString(fmt.Sprintf("[%s]\n", name))
		sb.WriteString(sources[name])
		sb.WriteString("\n\n")
	}

	resp, err := g.agent.executor.Complete(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: curatorSystemPrompt},
			{Role: "user", Content: sb.String()},
		},
		Tools:       []llm.ToolDef{curatorToolDef()},
		ToolChoice:  "required",
		Temperature: 0.0,
		MaxTokens:   2048,
	})
	if err != nil {
		return "", fmt.Errorf("curator LLM: %w", err)
	}

	raw, err := extractToolArgs(resp)
	if err != nil {
		return "", fmt.Errorf("curator: %w", err)
	}
	var parsed struct {
		Summary string `json:"summary"`
	}
	if err := ParseLLMJSON(raw, &parsed); err != nil {
		// Fallback: if extraction failed, use the raw text. Function-calling
		// should make this rare but defense in depth.
		log.Printf("[ctx] curator output not valid JSON, using raw: %v", err)
		return strings.TrimSpace(raw), nil
	}
	return parsed.Summary, nil
}

// ── Source helper constructors ──────────────────────────────────────────────
// These keep call sites clean. Prefer constructors over building SourceSpec
// structs by hand — they enforce the param shape per source.

// Blueprint returns a spec for the latest blueprint on disk.
func Blueprint() SourceSpec {
	return SourceSpec{Name: SourceBlueprint, Params: map[string]any{}}
}

// BlueprintNamed returns a spec for a blueprint by file path or basename.
// Empty name = latest.
func BlueprintNamed(name string) SourceSpec {
	return SourceSpec{Name: SourceBlueprint, Params: map[string]any{"name": name}}
}

// BlueprintSection returns a spec for a single ## section of the latest
// blueprint. The heading is the text after "## " (e.g. "Files", "Build System").
// If the section is missing, the source returns empty.
func BlueprintSection(heading string) SourceSpec {
	return SourceSpec{Name: SourceBlueprint, Params: map[string]any{
		"section": heading,
	}}
}

// BlueprintSections returns a spec for multiple ## sections of the latest
// blueprint, concatenated in the order requested. Missing sections are
// silently dropped. Useful when the caller wants several specific parts of
// the blueprint without the overhead of the whole document.
func BlueprintSections(headings ...string) SourceSpec {
	return SourceSpec{Name: SourceBlueprint, Params: map[string]any{
		"sections": headings,
	}}
}

// Worklog returns a spec for the worklog. filter: "all"|"failures"|"successes".
// TailTrunc is set so budget truncation keeps the newest entries (the end of
// the log), not the oldest.
func Worklog(lines int, filter string) SourceSpec {
	return SourceSpec{Name: SourceWorklog, Params: map[string]any{
		"lines":  lines,
		"filter": filter,
	}, TailTrunc: true}
}

// NodeReturns returns a spec for previously-executed node results.
// filter: "all"|"failures".
func NodeReturns(filter string) SourceSpec {
	return SourceSpec{Name: SourceNodeReturns, Params: map[string]any{"filter": filter}}
}

// NodeReturnsTyped narrows by node tool name (e.g. ["bash","compute"]).
func NodeReturnsTyped(filter string, types []string) SourceSpec {
	return SourceSpec{Name: SourceNodeReturns, Params: map[string]any{
		"filter": filter,
		"types":  types,
	}}
}

// WorkspaceTree returns a spec for the workspace file tree at the given depth.
func WorkspaceTree(maxDepth int) SourceSpec {
	return SourceSpec{Name: SourceWorkspaceTree, Params: map[string]any{
		"max_depth": maxDepth,
	}}
}

// WorkspaceTreeFocus narrows the tree scan to a subdirectory.
func WorkspaceTreeFocus(maxDepth int, focusDir string) SourceSpec {
	return SourceSpec{Name: SourceWorkspaceTree, Params: map[string]any{
		"max_depth": maxDepth,
		"focus_dir": focusDir,
	}}
}

// ServiceState returns a spec for all known services.
func ServiceState() SourceSpec {
	return SourceSpec{Name: SourceServiceState, Params: map[string]any{}}
}

// ServiceStateFiltered returns services matching a status. status: "running"|"crashed".
func ServiceStateFiltered(status string) SourceSpec {
	return SourceSpec{Name: SourceServiceState, Params: map[string]any{"status": status}}
}

// History returns a spec for recent conversation turns.
func History(turns int) SourceSpec {
	return SourceSpec{Name: SourceHistory, Params: map[string]any{"turns": turns}}
}

// SkillGuidance returns a spec for active skill guidance, optionally narrowed
// to topics like "Coder", "Architect", "Debug", "Aggregator", "Planning".
func SkillGuidance(topics []string) SourceSpec {
	return SourceSpec{Name: SourceSkillGuidance, Params: map[string]any{"topics": topics}}
}

// WorkspaceDeep returns a spec for the deep workspace scan: file tree plus
// the contents of small files. Architect-only — much heavier than WorkspaceTree.
func WorkspaceDeep(maxDepth int) SourceSpec {
	return SourceSpec{Name: SourceWorkspaceDeep, Params: map[string]any{"max_depth": maxDepth}}
}

// FunctionMapSpec returns a spec for the function declaration map: a list of
// function signatures discovered across the workspace, formatted for prompt
// injection. Architect-only.
func FunctionMapSpec(maxDepth int, maxBytes int) SourceSpec {
	return SourceSpec{Name: SourceFunctionMap, Params: map[string]any{
		"max_depth": maxDepth,
		"max_bytes": maxBytes,
	}}
}

// ExistingBlueprints returns a spec for ALL blueprints in the session's
// blueprints directory (not just the latest). Architect uses this to build
// on previous work within the same session.
func ExistingBlueprints() SourceSpec {
	return SourceSpec{Name: SourceExistingBlueprints, Params: map[string]any{}}
}

// Sources is a tiny convenience to build a slice in call sites:
//
//	gate.Get(req, Sources(Blueprint(), Worklog(20, "all")))
func Sources(specs ...SourceSpec) []SourceSpec {
	return specs
}

// ── Param helpers ───────────────────────────────────────────────────────────

func paramString(p map[string]any, key, def string) string {
	if p == nil {
		return def
	}
	if v, ok := p[key].(string); ok {
		return v
	}
	return def
}

func paramInt(p map[string]any, key string, def int) int {
	if p == nil {
		return def
	}
	switch v := p[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return def
}

func paramStringSlice(p map[string]any, key string) []string {
	if p == nil {
		return nil
	}
	switch v := p[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// ── Source implementations ──────────────────────────────────────────────────

// blueprintSource: latest blueprint or named.
type blueprintSource struct{}

func (s *blueprintSource) Name() string { return SourceBlueprint }

func (s *blueprintSource) Load(g *Graph, t *Trigger, a *Agent, params map[string]any) (string, error) {
	if a == nil || a.cfg.MetadataDir == "" {
		return "", nil
	}
	sid := ""
	if t != nil {
		sid = t.SessionID
	}

	content := s.loadContent(a, sid, paramString(params, "name", ""))
	if content == "" {
		return "", nil
	}

	// Optional focus: extract one or more named sections (## Heading).
	// Single section via "section" param; multiple via "sections" string slice.
	// Sections are returned in the order requested, separated by blank lines.
	if section := paramString(params, "section", ""); section != "" {
		return Text.ExtractSection(content, "## "+section), nil
	}
	if sections := paramStringSlice(params, "sections"); len(sections) > 0 {
		var parts []string
		for _, sec := range sections {
			extracted := Text.ExtractSection(content, "## "+sec)
			if extracted != "" {
				parts = append(parts, "## "+sec+"\n"+extracted)
			}
		}
		return strings.Join(parts, "\n\n"), nil
	}

	return content, nil
}

// loadContent resolves the blueprint name into raw markdown content. Empty
// name → latest blueprint for the session. Absolute path → direct read.
// Relative name → tries session blueprints dir, then metadata dir.
func (s *blueprintSource) loadContent(a *Agent, sid, name string) string {
	if name == "" {
		return loadLatestBlueprint(a.cfg.MetadataDir, sid)
	}
	if filepath.IsAbs(name) {
		data, err := os.ReadFile(name)
		if err != nil {
			return ""
		}
		return string(data)
	}
	candidate := filepath.Join(blueprintsDir(a.cfg.MetadataDir, sid), name)
	if _, err := os.Stat(candidate); err == nil {
		data, _ := os.ReadFile(candidate)
		return string(data)
	}
	candidate2 := filepath.Join(a.cfg.MetadataDir, name)
	if _, err := os.Stat(candidate2); err == nil {
		data, _ := os.ReadFile(candidate2)
		return string(data)
	}
	return ""
}

// worklogSource: last N lines of the worklog.
type worklogSource struct{}

func (s *worklogSource) Name() string { return SourceWorklog }

func (s *worklogSource) Load(g *Graph, t *Trigger, a *Agent, params map[string]any) (string, error) {
	if a == nil || a.cfg.MetadataDir == "" {
		return "", nil
	}
	lines := paramInt(params, "lines", 30)
	if lines <= 0 {
		return "", nil
	}
	sid := ""
	if t != nil {
		sid = t.SessionID
	}
	wl := readWorklog(a.cfg.MetadataDir, sid, lines)
	if wl == "" {
		return "", nil
	}
	filter := paramString(params, "filter", "all")
	if filter == "all" || filter == "" {
		return wl, nil
	}
	// Apply filter line by line. Cheap and good enough for Phase 1.
	var kept []string
	for _, line := range strings.Split(wl, "\n") {
		switch filter {
		case "failures":
			if containsAnyCI(line, "FAIL", "ERROR", "FAILED", "BLIND_RETRY", "ONESHOT", "REPLAN") {
				kept = append(kept, line)
			}
		case "successes":
			if containsAnyCI(line, "RESOLVED", "COMPLETED", "PASS", "CONTINUE") {
				kept = append(kept, line)
			}
		default:
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n"), nil
}

func containsAnyCI(s string, needles ...string) bool {
	upper := strings.ToUpper(s)
	for _, n := range needles {
		if strings.Contains(upper, strings.ToUpper(n)) {
			return true
		}
	}
	return false
}

// extractFailureDetail pulls the most useful error detail from a failed node.
// For bash nodes, it digs into the structured JSON to extract stdout/stderr
// instead of showing the raw JSON wrapper. Used by nodeReturnsSource and
// the aggregator's failure summary.
func extractFailureDetail(n *Node) string {
	// Try to extract from the result (bash errors are returned as result, not Error)
	raw := n.Result
	if raw == "" && n.Error != nil {
		raw = n.Error.Error()
	}
	if raw == "" {
		return "(no error detail)"
	}

	// Try to parse bash structured error
	var bashErr struct {
		BashError bool   `json:"bash_error"`
		Command   string `json:"command"`
		Stdout    string `json:"stdout"`
		Stderr    string `json:"stderr"`
		ExitCode  int    `json:"exit_code"`
	}
	if json.Unmarshal([]byte(raw), &bashErr) == nil && bashErr.BashError {
		var parts []string
		if bashErr.Stderr != "" {
			parts = append(parts, "stderr: "+bashErr.Stderr)
		}
		if bashErr.Stdout != "" {
			parts = append(parts, "stdout: "+bashErr.Stdout)
		}
		if len(parts) > 0 {
			return fmt.Sprintf("exit %d | %s", bashErr.ExitCode, strings.Join(parts, " | "))
		}
		return fmt.Sprintf("exit %d (command: %s)", bashErr.ExitCode, bashErr.Command)
	}

	// Fallback: raw error
	if n.Error != nil {
		return n.Error.Error()
	}
	return raw
}

// nodeReturnsSource: results from previously-executed nodes in the current graph.
type nodeReturnsSource struct{}

func (s *nodeReturnsSource) Name() string { return SourceNodeReturns }

func (s *nodeReturnsSource) Load(g *Graph, t *Trigger, a *Agent, params map[string]any) (string, error) {
	if g == nil {
		return "", nil
	}
	filter := paramString(params, "filter", "all")
	types := paramStringSlice(params, "types")
	typeSet := make(map[string]bool, len(types))
	for _, ty := range types {
		typeSet[ty] = true
	}

	var sb strings.Builder

	switch filter {
	case "failures":
		failed := g.FailedNodes()
		if len(failed) == 0 {
			return "", nil
		}
		sb.WriteString("The following steps FAILED. Address these — either investigate to find a fix, or conclude with an honest verdict.\n\n")
		for _, f := range failed {
			if len(typeSet) > 0 && !typeSet[f.ToolName] {
				continue
			}
			label := f.Tag
			if label == "" {
				label = f.ToolName
			}
			errMsg := Text.TailTruncate(extractFailureDetail(f), 1200)
			sb.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", label, f.ToolName, errMsg))
		}

	default: // "all" — resolved + failed
		resolved := g.ResolvedResultsSoFar()
		failed := g.FailedNodes()

		if len(resolved) == 0 && len(failed) == 0 {
			return "", nil
		}

		if len(resolved) > 0 {
			sb.WriteString("### Resolved\n\n")
			for label, result := range resolved {
				sb.WriteString(fmt.Sprintf("**%s:**\n%s\n\n", label, result))
			}
		}

		if len(failed) > 0 {
			sb.WriteString("### Failed\n\n")
			for _, f := range failed {
				if len(typeSet) > 0 && !typeSet[f.ToolName] {
					continue
				}
				label := f.Tag
				if label == "" {
					label = f.ToolName
				}
				errMsg := Text.TailTruncate(extractFailureDetail(f), 1200)
				sb.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", label, f.ToolName, errMsg))
			}
		}
	}

	return sb.String(), nil
}

// workspaceTreeSource: light scan of files on disk.
type workspaceTreeSource struct{}

func (s *workspaceTreeSource) Name() string { return SourceWorkspaceTree }

func (s *workspaceTreeSource) Load(g *Graph, t *Trigger, a *Agent, params map[string]any) (string, error) {
	if a == nil || a.cfg.Workspace == "" {
		return "", nil
	}
	depth := paramInt(params, "max_depth", 3)
	focus := paramString(params, "focus_dir", "")

	root := a.cfg.Workspace
	if focus != "" {
		// Resolve focus relative to workspace.
		if filepath.IsAbs(focus) {
			root = focus
		} else {
			root = filepath.Join(a.cfg.Workspace, focus)
		}
	}
	tree := scanWorkspaceTree(root, depth)
	if tree == "" {
		return "", nil
	}
	return "```\n" + tree + "```", nil
}

// serviceStateSource: reads .services.json registry directly.
type serviceStateSource struct{}

func (s *serviceStateSource) Name() string { return SourceServiceState }

func (s *serviceStateSource) Load(g *Graph, t *Trigger, a *Agent, params map[string]any) (string, error) {
	if a == nil || a.cfg.Workspace == "" {
		return "", nil
	}
	path := filepath.Join(a.cfg.Workspace, ".services.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil // missing file is normal
	}

	// The registry is an array of records — see internal/tools/service.go.
	var records []map[string]any
	if err := json.Unmarshal(data, &records); err != nil {
		return "", nil
	}
	if len(records) == 0 {
		return "", nil
	}

	statusFilter := paramString(params, "status", "")
	nameFilter := paramStringSlice(params, "names")
	nameSet := make(map[string]bool, len(nameFilter))
	for _, n := range nameFilter {
		nameSet[n] = true
	}

	var sb strings.Builder
	sb.WriteString("Known services:\n")
	count := 0
	for _, r := range records {
		name, _ := r["name"].(string)
		status, _ := r["status"].(string)
		if statusFilter != "" && status != statusFilter {
			continue
		}
		if len(nameSet) > 0 && !nameSet[name] {
			continue
		}
		port, _ := r["port"].(float64)
		pid, _ := r["pid"].(float64)
		cmd, _ := r["command"].(string)
		workdir, _ := r["workdir"].(string)
		sb.WriteString(fmt.Sprintf("- **%s** [%s] pid=%d port=%d workdir=%q command=%q\n",
			name, status, int(pid), int(port), workdir, cmd))
		count++
	}
	if count == 0 {
		return "", nil
	}
	return sb.String(), nil
}

// historySource: recent conversation turns from Trigger.History.
type historySource struct{}

func (s *historySource) Name() string { return SourceHistory }

func (s *historySource) Load(g *Graph, t *Trigger, a *Agent, params map[string]any) (string, error) {
	if t == nil || len(t.History) == 0 {
		return "", nil
	}
	turns := paramInt(params, "turns", 5)
	if turns <= 0 {
		return "", nil
	}
	// Take the last N messages.
	start := 0
	if len(t.History) > turns {
		start = len(t.History) - turns
	}
	var sb strings.Builder
	for _, m := range t.History[start:] {
		sb.WriteString(fmt.Sprintf("[%s] %s\n", m.Role, m.Content))
	}
	return sb.String(), nil
}

// (No memorySource — memory is intentionally not exposed via ContextGate.
// See the architectural doc at the top of this file. Memory lives at the
// chat boundary in api.go and memory.Manager, NOT in the execution layer.)

// skillGuidanceSource: extracts named topics from active skill cards.
type skillGuidanceSource struct{}

func (s *skillGuidanceSource) Name() string { return SourceSkillGuidance }

func (s *skillGuidanceSource) Load(g *Graph, t *Trigger, a *Agent, params map[string]any) (string, error) {
	if a == nil || g == nil || len(g.ActiveCards) == 0 {
		return "", nil
	}
	topics := paramStringSlice(params, "topics")
	if len(topics) == 0 {
		// Default: just Debug Guidance (the most useful for runtime)
		topics = []string{"Debug Guidance"}
	}

	var parts []string
	for _, key := range g.ActiveCards {
		body, name := a.lookupGuidanceBody(key)
		if body == "" {
			continue
		}
		var topicParts []string
		for _, topic := range topics {
			heading := topic
			if !strings.HasPrefix(heading, "## ") {
				heading = "## " + heading
				if !strings.Contains(heading, "Guidance") {
					heading = heading + " Guidance"
				}
			}
			section := Text.ExtractSection(body, heading)
			if section != "" {
				topicParts = append(topicParts, fmt.Sprintf("#### %s\n%s", strings.TrimPrefix(heading, "## "), section))
			}
		}
		if len(topicParts) > 0 {
			parts = append(parts, fmt.Sprintf("### %s\n%s", name, strings.Join(topicParts, "\n\n")))
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	return strings.Join(parts, "\n\n"), nil
}

// workspaceDeepSource: file tree + small file contents + function signatures.
// Architect-only — heavy. Wraps scanWorkspaceDeep.
type workspaceDeepSource struct{}

func (s *workspaceDeepSource) Name() string { return SourceWorkspaceDeep }

func (s *workspaceDeepSource) Load(g *Graph, t *Trigger, a *Agent, params map[string]any) (string, error) {
	if a == nil || a.cfg.Workspace == "" {
		return "", nil
	}
	depth := paramInt(params, "max_depth", 4)
	scan := scanWorkspaceDeep(a.cfg.Workspace, depth)
	return scan, nil
}

// functionMapSource: discovered function declarations across the workspace.
// Architect-only.
type functionMapSource struct{}

func (s *functionMapSource) Name() string { return SourceFunctionMap }

func (s *functionMapSource) Load(g *Graph, t *Trigger, a *Agent, params map[string]any) (string, error) {
	if a == nil || a.cfg.Workspace == "" {
		return "", nil
	}
	depth := paramInt(params, "max_depth", 5)
	maxBytes := paramInt(params, "max_bytes", 16000)
	fm := ScanFunctionMap(a.cfg.Workspace, depth)
	return FormatFunctionMapForPrompt(fm, maxBytes), nil
}

// existingBlueprintsSource: ALL blueprints in the session's blueprints
// directory. Architect-only — different from blueprintSource which returns
// only the latest single blueprint.
type existingBlueprintsSource struct{}

func (s *existingBlueprintsSource) Name() string { return SourceExistingBlueprints }

func (s *existingBlueprintsSource) Load(g *Graph, t *Trigger, a *Agent, params map[string]any) (string, error) {
	if a == nil || a.cfg.MetadataDir == "" {
		return "", nil
	}
	sid := ""
	if t != nil {
		sid = t.SessionID
	}
	return scanExistingBlueprints(a.cfg.MetadataDir, sid), nil
}
