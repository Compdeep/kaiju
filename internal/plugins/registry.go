// Package plugins is an optional, build-tag-gated extension point for kaiju.
//
// A plugin bundles one or more tools that are compiled into the binary ONLY when
// its build tag is set (e.g. `go build -tags plugin_pdf`) and switched on at
// runtime only when named in config `plugins` or the `--plugins` flag. This keeps
// heavy or niche dependencies (PDF parsing, etc.) out of the default binary while
// letting an operator opt in without forking the codebase.
//
// A plugin registers itself from an init() in a build-tagged file, so the default
// build links in neither the plugin nor its dependencies — Compiled() comes back
// empty and the wiring in main.go is a no-op.
package plugins

import (
	"sort"
	"sync"

	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
)

// Deps are the shared services a plugin's tools may need at construction time.
// Extend this as new plugins need more (executor client, memory, …); a plugin
// simply ignores the fields it doesn't use.
type Deps struct {
	Workspace string // sandbox root; file-touching tools resolve paths under it
}

// Plugin contributes a named bundle of tools to the agent's tool registry.
type Plugin interface {
	// Name is the activation key used in config `plugins` / the `--plugins` flag.
	Name() string
	// Tools builds this plugin's tools. Called once at startup, and only when the
	// plugin is both compiled in and activated.
	Tools(Deps) []agenttools.Tool
}

var (
	mu         sync.Mutex
	registered = map[string]Plugin{}
)

// Register records a compiled-in plugin. Call it from an init() in the plugin's
// build-tagged file so the default build never links the plugin in.
func Register(p Plugin) {
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

// Activate builds the tools of every plugin named in `want` that is compiled in.
// It returns the tools to register, the plugin names actually switched on, and
// any requested-but-not-compiled-in names so the caller can warn the operator
// (they asked for a plugin this binary wasn't built with).
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
		active = append(active, p.Tools(d)...)
		on = append(on, name)
	}
	return active, on, missing
}
