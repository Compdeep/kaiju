package agent

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	agenttools "github.com/user/kaiju/internal/agent/tools"
	"github.com/user/kaiju/internal/db"
)

/*
 * IntentRegistry is the in-memory snapshot of the intent registry loaded
 * from the database at agent startup.
 * desc: Provides name↔rank resolution, per-tool intent override lookup,
 *       and prompt-block generation for preflight and planner prompts.
 *       Immutable after Load() — changes to the DB require agent restart.
 */
type IntentRegistry struct {
	mu            sync.RWMutex
	byName        map[string]db.Intent
	byRank        map[int]db.Intent
	ordered       []db.Intent       // sorted by rank ascending
	toolOverrides map[string]string // tool name → intent name
}

/*
 * NewIntentRegistry constructs an empty registry.
 */
func NewIntentRegistry() *IntentRegistry {
	return &IntentRegistry{
		byName:        make(map[string]db.Intent),
		byRank:        make(map[int]db.Intent),
		toolOverrides: make(map[string]string),
	}
}

/*
 * Load populates the registry from the database.
 * desc: Reads intents + tool_intents. Must be called at agent startup
 *       after database migrations have run (so defaults are seeded).
 */
func (r *IntentRegistry) Load(database *db.DB) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	intents, err := database.ListIntents()
	if err != nil {
		return fmt.Errorf("load intents: %w", err)
	}
	r.byName = make(map[string]db.Intent, len(intents))
	r.byRank = make(map[int]db.Intent, len(intents))
	r.ordered = make([]db.Intent, 0, len(intents))
	for _, i := range intents {
		r.byName[i.Name] = i
		r.byRank[i.Rank] = i
		r.ordered = append(r.ordered, i)
	}
	sort.Slice(r.ordered, func(a, b int) bool { return r.ordered[a].Rank < r.ordered[b].Rank })

	overrides, err := database.ListToolIntents()
	if err != nil {
		return fmt.Errorf("load tool_intents: %w", err)
	}
	r.toolOverrides = overrides
	return nil
}

/*
 * ByName resolves an intent name to its full record. Returns false if unknown.
 */
func (r *IntentRegistry) ByName(name string) (db.Intent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	i, ok := r.byName[strings.ToLower(strings.TrimSpace(name))]
	return i, ok
}

/*
 * RankByName returns the rank integer for an intent name, or -1 if unknown.
 */
func (r *IntentRegistry) RankByName(name string) int {
	if i, ok := r.ByName(name); ok {
		return i.Rank
	}
	return -1
}

/*
 * NameByRank returns the canonical intent name for a rank value.
 */
func (r *IntentRegistry) NameByRank(rank int) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if i, ok := r.byRank[rank]; ok {
		return i.Name
	}
	return ""
}

/*
 * List returns all intents ordered by rank ascending.
 */
func (r *IntentRegistry) List() []db.Intent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]db.Intent, len(r.ordered))
	copy(out, r.ordered)
	return out
}

/*
 * ListAllowed returns intents with rank ≤ maxRank, ordered ascending.
 * desc: Used by prompt builders to show only intents the current scope
 *       permits. maxRank of -1 returns all.
 */
func (r *IntentRegistry) ListAllowed(maxRank int) []db.Intent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]db.Intent, 0, len(r.ordered))
	for _, i := range r.ordered {
		if maxRank < 0 || i.Rank <= maxRank {
			out = append(out, i)
		}
	}
	return out
}

/*
 * ResolveToolIntent returns the effective intent rank for a tool invocation.
 * desc: DB pin wins. Falls back to the tool's Go Impact() method, whose
 *       return value is already a rank on the same scale as the registry
 *       (see internal/agent/tools/skill.go — ImpactObserve/Affect/Control
 *       are ranks aligned with the three builtin intents). Unknown pin
 *       names fall back to the compiled default with a warning.
 * param: toolName - the tool's registered name.
 * param: tool - the Tool implementation (for fallback Impact() call).
 * param: params - invocation params (passed to tool.Impact).
 * return: the effective rank for gate comparison.
 */
func (r *IntentRegistry) ResolveToolIntent(toolName string, tool agenttools.Tool, params map[string]any) int {
	if tool == nil {
		return 0
	}
	compiled := tool.Impact(params)

	r.mu.RLock()
	pin, hasPin := r.toolOverrides[toolName]
	r.mu.RUnlock()

	if hasPin {
		if i, ok := r.ByName(pin); ok {
			// Pin is a ceiling — compiled per-invocation impact still applies,
			// but cannot exceed the pinned rank. This preserves granularity:
			// bash("ls")=0 stays 0 even if bash is pinned to rank 200.
			if compiled < i.Rank {
				return compiled
			}
			return i.Rank
		}
		fmt.Printf("[intent] tool %q has unknown pin %q, falling back to compiled default\n", toolName, pin)
	}

	return compiled
}

/*
 * PromptBlock returns a markdown bullet list of allowed intents with
 * their prompt descriptions, ready to inject into preflight and planner
 * prompts.
 * desc: Filters by maxRank (use -1 for no filter). Each bullet is
 *       '- "name": description'.
 */
func (r *IntentRegistry) PromptBlock(maxRank int) string {
	intents := r.ListAllowed(maxRank)
	var sb strings.Builder
	for _, i := range intents {
		sb.WriteString(fmt.Sprintf("- \"%s\": %s\n", i.Name, i.PromptDescription))
	}
	return sb.String()
}

/*
 * AllowedNames returns the list of intent names at or below maxRank,
 * used to build JSON schema enum fields.
 */
func (r *IntentRegistry) AllowedNames(maxRank int) []string {
	intents := r.ListAllowed(maxRank)
	out := make([]string, len(intents))
	for i, it := range intents {
		out[i] = it.Name
	}
	return out
}

/*
 * DefaultRank returns the rank of the intent flagged as default in the
 * config/DB. This is the "default working level" used for agent clearance,
 * CLI commands, and request parsers when no explicit intent is given.
 * The default is chosen explicitly by the admin (one intent carries
 * is_default=true) — it is NOT derived from list ordering, so adding or
 * removing custom intents never shifts it.
 *
 * If no intent is flagged (misconfigured install), returns 0 with a log
 * warning so the operator sees it in startup output. 0 is the safest
 * possible fallback — it effectively denies all side-effecting tools
 * until the config is fixed.
 */
func (r *IntentRegistry) DefaultRank() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, i := range r.ordered {
		if i.IsDefault {
			return i.Rank
		}
	}
	if len(r.ordered) > 0 {
		fmt.Printf("[intent] WARNING: no intent flagged as default — returning 0 (deny). Mark one intent with is_default=true in your config.\n")
	}
	return 0
}

/*
 * SnapUp returns the smallest registered intent rank ≥ minRank, or the
 * highest available rank if every registered intent is below minRank.
 * desc: Used by intent inference — when a plan's max tool impact is N,
 *       the effective intent must be at a registered rank ≥ N so that
 *       tools at that impact pass the gate. Any admin-configured intent
 *       participates naturally: an intent at rank 50 is picked when the
 *       plan's heaviest tool resolves to rank 40.
 * param: minRank - the floor rank to snap up from.
 * return: the snapped rank, or 0 if the registry is empty.
 */
func (r *IntentRegistry) SnapUp(minRank int) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.ordered) == 0 {
		return 0
	}
	for _, i := range r.ordered {
		if i.Rank >= minRank {
			return i.Rank
		}
	}
	return r.ordered[len(r.ordered)-1].Rank
}

/*
 * MaxRank returns the highest rank in the registry. Used by scope
 * resolution to cap requests at the highest known intent.
 */
func (r *IntentRegistry) MaxRank() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.ordered) == 0 {
		return 0
	}
	return r.ordered[len(r.ordered)-1].Rank
}

