package toolapi

import (
	"fmt"
	"sort"
	"sync"

	"github.com/Compdeep/kaiju/agent/llm"
)

/*
 * registeredTool wraps a Tool with metadata for dashboard queries.
 * desc: Internal wrapper that pairs a tool with its source tag and enabled flag
 */
type registeredTool struct {
	tool   Tool
	source string // "builtin" or "skillmd:<path>"
	reach  Reach  // how far this tool may be called from
}

// Reach is how far a registered tool may be called from.
//
// One value rather than a flag and a second list. An application that can run a
// step on another machine has three answers for each tool, not two: nobody may
// call it, this node may call it on itself, or another machine may ask this
// node to run it. The middle one is the interesting case and it has nowhere to
// live in a boolean — a tool that is fine to run here can be a poor thing to
// let a stranger trigger.
//
// Reach says nothing about whether THIS node may call OUT to another machine.
// That is the plan's business and the remote executor's. This is only about
// what arrives here.
type Reach uint8

const (
	// ReachOff is registered and callable by nobody. The tool still appears in
	// a listing, which is the difference between off and absent: an operator
	// can see it exists and turn it on.
	ReachOff Reach = iota

	// ReachLocal is callable by this node's own runs. The default for a newly
	// registered tool, because granting more should be something someone typed.
	ReachLocal

	// ReachEverywhere is callable by this node's runs and by another machine
	// dispatching a step onto it.
	ReachEverywhere
)

// String is the word the API and the dashboard use.
func (r Reach) String() string {
	switch r {
	case ReachLocal:
		return "local"
	case ReachEverywhere:
		return "everywhere"
	}
	return "off"
}

// ParseReach reads the word back, and reports whether it was one.
func ParseReach(s string) (Reach, bool) {
	switch s {
	case "off":
		return ReachOff, true
	case "local":
		return ReachLocal, true
	case "everywhere":
		return ReachEverywhere, true
	}
	return ReachOff, false
}

/*
 * Registry is a thread-safe tool registry.
 * desc: Central store for all registered tools, supporting lookup, hot-reload, and dashboard queries
 */
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*registeredTool

	// version changes whenever the tools do — one added, one removed, one
	// swapped for another under the same name, one switched off.
	//
	// Here rather than derived by a caller because a caller cannot derive it. A
	// list of names does not change when a hot reload replaces a tool with a
	// new description under the old name, and anything holding a view of this
	// registry would go on serving what the tool used to say. See
	// Registry.Version.
	version uint64
}

/*
 * NewRegistry creates an empty Registry.
 * desc: Initializes a Registry with an empty tool map
 * return: a pointer to the new Registry
 */
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]*registeredTool),
	}
}

/*
 * Register adds a tool to the registry with source "builtin".
 * desc: Convenience method that delegates to RegisterWithSource with source set to "builtin"
 * param: t - the tool to register
 * return: an error if a tool with the same name is already registered
 */
func (r *Registry) Register(t Tool) error {
	return r.RegisterWithSource(t, "builtin")
}

/*
 * RegisterWithSource adds a tool to the registry with an explicit source tag.
 * desc: Inserts the tool into the registry, returning an error on name collision
 * param: t - the tool to register
 * param: source - the origin tag (e.g. "builtin", "skillmd:<path>")
 * return: an error if a tool with the same name is already registered
 */
func (r *Registry) RegisterWithSource(t Tool, source string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[t.Name()]; exists {
		return fmt.Errorf("tool %q already registered", t.Name())
	}
	r.tools[t.Name()] = &registeredTool{tool: t, source: source, reach: ReachLocal}
	r.version++
	return nil
}

/*
 * Replace atomically swaps a tool. Used by hot-reload watcher.
 * desc: Overwrites an existing tool entry or creates a new one. A tool that was
 *       already there keeps its reach: editing the file a tool is defined in is
 *       not a statement about who may call it, and resetting it would put back
 *       a tool an operator had switched off, on the next save, with nothing
 *       said. A tool arriving for the first time is local, as at registration.
 * param: t - the replacement tool
 * param: source - the origin tag for the replacement
 */
func (r *Registry) Replace(t Tool, source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reach := ReachLocal
	if existing, ok := r.tools[t.Name()]; ok {
		reach = existing.reach
	}
	r.tools[t.Name()] = &registeredTool{tool: t, source: source, reach: reach}
	r.version++
}

/*
 * Unregister removes a tool by name. No-op if not found.
 * desc: Deletes the named tool from the registry
 * param: name - the tool name to remove
 */
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; !ok {
		return
	}
	delete(r.tools, name)
	r.version++
}

/*
 * Get retrieves a tool by name.
 * desc: Looks up a tool and returns it only if it exists and is enabled
 * param: name - the tool name to look up
 * return: the Tool and true if found and enabled, or nil and false otherwise
 */
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.tools[name]
	if !ok || rt.reach < ReachLocal {
		return nil, false
	}
	return rt.tool, true
}

/*
 * GetSource returns the source tag for a registered tool.
 * desc: Retrieves the origin tag (e.g. "builtin", "skillmd", "custom") for the named tool
 * param: name - the tool name to look up
 * return: the source string, or empty string if not found
 */
func (r *Registry) GetSource(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.tools[name]
	if !ok {
		return ""
	}
	return rt.source
}

/*
 * IsBuiltin returns true if the named tool is a compiled builtin.
 * desc: Checks whether the tool's source tag is "builtin"
 * param: name - the tool name to check
 * return: true if the tool exists and its source is "builtin"
 */
func (r *Registry) IsBuiltin(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.tools[name]
	return ok && rt.source == "builtin"
}

/*
 * Version reports a number that changes whenever the tools do.
 * desc: Incremented by every registration, replacement, removal and enable
 *       toggle. It exists so a component holding a derived view of this
 *       registry — a search index, a cached prompt fragment — can tell in one
 *       comparison whether that view is still current, without rebuilding it
 *       to find out and without missing a tool swapped in under a name it
 *       already knows.
 * return: the current version. Only equality is meaningful; the value itself
 *         means nothing and does not survive a restart.
 */
func (r *Registry) Version() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.version
}

/*
 * List returns all registered tool names (including disabled).
 * desc: Collects every tool name in the registry regardless of enabled state
 * return: a slice of tool name strings
 */
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

/*
 * ToolInfo is the dashboard-facing metadata struct.
 * desc: Serializable snapshot of a tool's key attributes for the management dashboard API
 */
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Impact      int    `json:"impact"` // IGX impact tier index (0/1/2); registry maps to ranks on the configured ladder
	IsBuiltin   bool   `json:"isBuiltin"`
	Enabled     bool   `json:"enabled"`
	Reach       string `json:"reach"` // off | local | everywhere
	Source      string `json:"source"`
}

/*
 * ListInfo returns enriched metadata for all tools (for dashboard API).
 * desc: Builds a sorted slice of ToolInfo structs covering every registered tool
 * return: a slice of ToolInfo sorted alphabetically by tool name
 */
func (r *Registry) ListInfo() []ToolInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	infos := make([]ToolInfo, 0, len(r.tools))
	for _, rt := range r.tools {
		infos = append(infos, ToolInfo{
			Name:        rt.tool.Name(),
			Description: rt.tool.Description(),
			Impact:      GetImpact(rt.tool, nil),
			IsBuiltin:   rt.source == "builtin",
			Enabled:     rt.reach != ReachOff,
			Reach:       rt.reach.String(),
			Source:      rt.source,
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

/*
 * SetEnabled toggles a tool on/off from the dashboard.
 * desc: Updates the enabled flag for the named tool
 * param: name - the tool name to toggle
 * param: enabled - true to enable, false to disable
 * return: an error if the tool is not found
 */
func (r *Registry) SetEnabled(name string, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rt, ok := r.tools[name]
	if !ok {
		return fmt.Errorf("tool %q not found", name)
	}
	// On means local, never everywhere: a two-state caller cannot be allowed to
	// grant another machine the right to run something here, because it has no
	// way to say it meant to. Use SetReach for that.
	if enabled {
		rt.reach = ReachLocal
	} else {
		rt.reach = ReachOff
	}
	r.version++
	return nil
}

/*
 * AllToolDefs returns OpenAI tool definitions for all enabled tools.
 * desc: Converts every enabled tool to an llm.ToolDef for inclusion in LLM requests
 * return: a slice of llm.ToolDef for all enabled tools
 */
func (r *Registry) AllToolDefs() []llm.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]llm.ToolDef, 0, len(r.tools))
	for _, rt := range r.tools {
		if rt.reach != ReachOff {
			defs = append(defs, ToToolDef(rt.tool))
		}
	}
	return defs
}

/*
 * ToolDefsForNames returns tool definitions only for the named enabled tools.
 * desc: Filters to the requested names in input order, silently skipping unknown or disabled tools
 * param: names - the tool names to include
 * return: a slice of llm.ToolDef in the same order as the input names
 */
func (r *Registry) ToolDefsForNames(names []string) []llm.ToolDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]llm.ToolDef, 0, len(names))
	for _, name := range names {
		if rt, ok := r.tools[name]; ok && rt.reach != ReachOff {
			defs = append(defs, ToToolDef(rt.tool))
		}
	}
	return defs
}

/*
 * GetForRemote returns a tool only if another machine may run it here.
 * desc: The lookup a handler uses for work arriving from elsewhere. A tool at
 *       ReachLocal is invisible to it and the call fails as an unknown tool,
 *       which is what an operator means by "this node may do that, and nobody
 *       else may make it".
 * param: name - the tool name from the incoming request.
 * return: the tool and true when it may be run for another machine.
 */
func (r *Registry) GetForRemote(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.tools[name]
	if !ok || rt.reach < ReachEverywhere {
		return nil, false
	}
	return rt.tool, true
}

/*
 * SetReach sets how far a tool may be called from.
 * desc: The three-state form of SetEnabled, for a caller that can say which of
 *       the two "on" states it means.
 * param: name - the registered tool.
 * param: reach - off, local, or everywhere.
 * return: an error naming the tool when it is not registered.
 */
func (r *Registry) SetReach(name string, reach Reach) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rt, ok := r.tools[name]
	if !ok {
		return fmt.Errorf("tool %q is not registered", name)
	}
	rt.reach = reach
	return nil
}

/*
 * ReachOf returns how far a tool may be called from.
 * desc: ReachOff for a tool that is not registered at all, since neither can
 *       be called and a caller asking this question wants to know whether it
 *       may run, not why it may not.
 */
func (r *Registry) ReachOf(name string) Reach {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if rt, ok := r.tools[name]; ok {
		return rt.reach
	}
	return ReachOff
}
