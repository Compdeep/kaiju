// Package plugins is an optional, build-tag-gated extension point for kaiju.
//
// A plugin bundles capabilities that are compiled into the binary ONLY when its
// build tag is set (e.g. `go build -tags plugin_pdf`) and switched on at runtime
// only when named in config `plugins` or the `--plugins` flag. This keeps heavy
// or niche dependencies (PDF parsing, etc.) out of the default binary while
// letting an operator opt in without forking the codebase.
//
// A plugin adds itself from an init() in a build-tagged file (plugins.Add), so
// the default build links in neither the plugin nor its dependencies. At startup
// Activate calls each active plugin's Register(Host), through which it explicitly
// contributes its capabilities.
package plugins

import (
	"context"
	"sort"
	"sync"

	agenttools "github.com/Compdeep/kaiju/agent/tools"
)

// Deps are the shared services a Host is built from. Extend this as plugins need
// more (executor client, memory, …); a plugin reads only what it needs, through
// the Host.
type Deps struct {
	Workspace string // sandbox root; file-touching tools resolve paths under it
}

// Host is the surface a plugin registers its capabilities into, at activation. A
// plugin touches ONLY the Host — never global state — so what it contributes is
// explicit at the call site and capturable in a test. Two kinds of capability:
//
//   - a TOOL the agent's planner can call by name (AddTool);
//   - a SEAM that enriches an existing core tool and is called by that tool, not
//     the planner (RegisterBinaryDecoder, RegisterReaderFallback).
//
// Grow this interface as new seams appear (a skill registrar, a renderer, …); a
// plugin uses only the methods it needs.
type Host interface {
	// Workspace is the sandbox root file-touching tools resolve paths under.
	Workspace() string
	// AddTool contributes a tool the agent's planner can call by name.
	AddTool(agenttools.Tool)
	// RegisterBinaryDecoder teaches core web_fetch to turn a typed body (e.g.
	// "application/pdf") into text — invoked by the tool, not the planner.
	RegisterBinaryDecoder(mime string, fn func([]byte) (string, error))
	// RegisterReaderFallback teaches core web_fetch a heavier re-read path for a
	// URL with no extractable content (render + extract).
	RegisterReaderFallback(fn func(ctx context.Context, rawURL string) (string, error))
}

// Plugin contributes tools and/or seams to the agent. Register is called once at
// startup, and only when the plugin is both compiled in and activated.
type Plugin interface {
	// Name is the activation key used in config `plugins` / the `--plugins` flag.
	Name() string
	// Description is a one-line summary of what the plugin adds — surfaced by the
	// plugin_list tool so a user (via the agent) can see what's available.
	Description() string
	// Register contributes the plugin's capabilities through the Host.
	Register(Host)
}

var (
	mu         sync.Mutex
	registered = map[string]Plugin{}
	activeSet  = map[string]bool{} // plugins whose Register has run (boot or runtime)
)

// Add records a compiled-in plugin. Call it from an init() in the plugin's
// build-tagged file so the default build never links the plugin in.
func Add(p Plugin) {
	mu.Lock()
	defer mu.Unlock()
	registered[p.Name()] = p
}

// Compiled returns the names of every plugin compiled into this binary, sorted.
func Compiled() []string {
	mu.Lock()
	defer mu.Unlock()
	names := make([]string, 0, len(registered))
	for n := range registered {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Get returns a compiled-in plugin by name (for runtime activation).
func Get(name string) (Plugin, bool) {
	mu.Lock()
	defer mu.Unlock()
	p, ok := registered[name]
	return p, ok
}

// IsActive reports whether a plugin's Register has run (at boot or at runtime).
func IsActive(name string) bool {
	mu.Lock()
	defer mu.Unlock()
	return activeSet[name]
}

// MarkActive records a plugin as active. Runtime activation (plugin_enable) calls
// it after running a plugin's Register with a live host, so IsActive/Catalog
// reflect plugins switched on after boot.
func MarkActive(name string) {
	mu.Lock()
	defer mu.Unlock()
	activeSet[name] = true
}

// Info describes a compiled-in plugin for the plugin_list tool.
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Active      bool   `json:"active"`
}

// Catalog lists every compiled-in plugin with its description and active state,
// sorted by name — the read model behind plugin_list.
func Catalog() []Info {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Info, 0, len(registered))
	for name, p := range registered {
		out = append(out, Info{Name: name, Description: p.Description(), Active: activeSet[name]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RemoteInfo describes a remote (out-of-process) plugin surfaced to the user by
// NAME — so a user enables "webreader", never the "remote" bridge. These appear in
// plugin_list even when their host isn't running; enabling one points the bridge at
// its host and activates it, and the bridge stays invisible.
type RemoteInfo struct {
	Name        string
	Description string
	DefaultURL  string // where its host runs unless overridden by config remote_plugin_host
	StartCmd    string // shell command to LAUNCH the host when it isn't running ({port} is substituted); overridable by config remote_plugin_start. Empty ⇒ can't auto-start, only connect.
}

// RemoteCatalog is the curated set of known remote plugins. The bridge itself
// ("remote") is never listed — it's infrastructure, not a capability.
var RemoteCatalog = []RemoteInfo{
	{
		Name:        "webreader",
		Description: "Read web pages as clean text, rendering JavaScript-heavy pages (SPAs, dashboards) when a plain fetch comes back thin. Once on, web_fetch reads every page through it.",
		DefaultURL:  "http://127.0.0.1:8092", // 8091 is the MCS worker; keep off it
		StartCmd:    "/home/sites/kaiju/kaiju/plugins/start.sh {port}",
	},
}

// RemoteByName returns a known remote plugin by name.
func RemoteByName(name string) (RemoteInfo, bool) {
	for _, r := range RemoteCatalog {
		if r.Name == name {
			return r, true
		}
	}
	return RemoteInfo{}, false
}

// Activate registers the capabilities of every plugin named in `want` that is
// compiled in. It returns the tools to add to the agent registry, the plugin
// names actually switched on, and any requested-but-not-compiled-in names so the
// caller can warn the operator (they asked for a plugin this binary wasn't built
// with). Seams register as a side effect through each plugin's Host.
func Activate(want []string, d Deps) (active []agenttools.Tool, on, missing []string) {
	mu.Lock()
	defer mu.Unlock()
	seen := map[string]bool{}
	for _, name := range want {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		p, ok := registered[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		h := &activation{deps: d}
		p.Register(h)
		activeSet[name] = true
		active = append(active, h.tools...)
		on = append(on, name)
	}
	return active, on, missing
}

// activation is the concrete Host used during Activate: it accumulates the tools
// a plugin adds and delegates seam registration to the core agent/tools package.
type activation struct {
	deps  Deps
	tools []agenttools.Tool
}

func (a *activation) Workspace() string         { return a.deps.Workspace }
func (a *activation) AddTool(t agenttools.Tool) { a.tools = append(a.tools, t) }

func (a *activation) RegisterBinaryDecoder(mime string, fn func([]byte) (string, error)) {
	agenttools.RegisterBinaryDecoder(mime, fn)
}

func (a *activation) RegisterReaderFallback(fn func(ctx context.Context, rawURL string) (string, error)) {
	agenttools.RegisterReaderFallback(fn)
}
