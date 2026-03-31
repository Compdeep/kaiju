package tools

import (
	"fmt"
	"sort"
	"sync"

	"github.com/user/kaiju/internal/agent/llm"
)

/*
 * registeredTool wraps a Tool with metadata for dashboard queries.
 * desc: Internal wrapper that pairs a tool with its source tag and enabled flag
 */
type registeredTool struct {
	tool    Tool
	source  string // "builtin" or "skillmd:<path>"
	enabled bool   // can be toggled from dashboard
}

/*
 * Registry is a thread-safe tool registry.
 * desc: Central store for all registered tools, supporting lookup, hot-reload, and dashboard queries
 */
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*registeredTool
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
	r.tools[t.Name()] = &registeredTool{tool: t, source: source, enabled: true}
	return nil
}

/*
 * Replace atomically swaps a tool. Used by hot-reload watcher.
 * desc: Overwrites an existing tool entry or creates a new one; always sets enabled to true
 * param: t - the replacement tool
 * param: source - the origin tag for the replacement
 */
func (r *Registry) Replace(t Tool, source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = &registeredTool{tool: t, source: source, enabled: true}
}

/*
 * Unregister removes a tool by name. No-op if not found.
 * desc: Deletes the named tool from the registry
 * param: name - the tool name to remove
 */
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
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
	if !ok || !rt.enabled {
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
	return names
}

/*
 * ToolInfo is the dashboard-facing metadata struct.
 * desc: Serializable snapshot of a tool's key attributes for the management dashboard API
 */
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Impact      int    `json:"impact"` // IBE impact tier: 0=observe, 1=affect, 2=control
	IsBuiltin   bool   `json:"isBuiltin"`
	Enabled     bool   `json:"enabled"`
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
			Enabled:     rt.enabled,
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
	rt.enabled = enabled
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
		if rt.enabled {
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
		if rt, ok := r.tools[name]; ok && rt.enabled {
			defs = append(defs, ToToolDef(rt.tool))
		}
	}
	return defs
}
