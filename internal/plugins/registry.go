// Package plugins is an optional, build-tag-gated extension point for kaiju.
//
// A plugin bundles capabilities that are compiled into the binary only when its
// build tag is set (e.g. `go build -tags plugin_pdf`). This keeps heavy or niche
// dependencies out of the default binary while letting an operator opt in without
// forking the codebase.
//
// There is ONE state: a plugin named in config `plugins` or the `--plugins` flag
// is in the binary and live, or it is not installed. Converge (converge.go) makes
// that true by rebuilding when the two disagree. There used to be a third state —
// compiled in but switched off — with a tool to leave it, a second Host to leave
// it at run time, and a flag per plugin recording which state it was in. Nothing
// needed it, and while it existed plugin_list could not name a plugin that was
// actually running.
//
// A plugin adds itself from an init() in a build-tagged file (plugins.Add), so
// the default build links in neither the plugin nor its dependencies. At startup
// Activate calls each active plugin's Register(Host), through which it explicitly
// contributes its capabilities.
package plugins

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Deps are the shared services a Host is built from. Extend this as plugins need
// more (executor client, memory, …); a plugin reads only what it needs, through
// the Host.
type Deps struct {
	Workspace string // sandbox root; file-touching tools resolve paths under it
	// AddSkill receives a plugin's planning guidance. A function rather than an
	// interface so this package stays independent of the agent package.
	AddSkill func(name, markdown string)
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
	AddTool(toolapi.Tool)
	// RegisterBinaryDecoder teaches core web_fetch to turn a typed body (e.g.
	// "application/pdf") into text — invoked by the tool, not the planner.
	RegisterBinaryDecoder(mime string, fn func([]byte) (string, error))
	// RegisterReaderFallback teaches core web_fetch a heavier re-read path for a
	// URL with no extractable content (render + extract).
	RegisterReaderFallback(fn func(ctx context.Context, rawURL string) (string, error))
	// AddSkill contributes the plugin's planning guidance — the "when and how"
	// card that sits beside its tools. A tool description says what a tool does;
	// the skill says when to reach for it and in what order. Without this the
	// bridge fetched each plugin's skill and logged that it was carrying it, so
	// the illustrator plugin's instruction to probe an image before editing it
	// never reached the planner that needed it.
	AddSkill(name, markdown string)
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
)

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

// Add records a compiled-in plugin. Call it from an init() in the plugin's
// build-tagged file so the default build never links the plugin in.
func Add(p Plugin) {
	mu.Lock()
	defer mu.Unlock()
	registered[p.Name()] = p
}

/*
 * PluginsDir locates the reference host's directory, or "" when there is none.
 * desc: Probed rather than configured, because the one place it was written down
 *       was an absolute path to the machine it was written on.
 *
 *       Beside the binary first, then the working directory — which covers a
 *       checkout run in place and a deployment that ships the folder next to the
 *       executable. A binary with neither returns "", and a caller that cannot
 *       find the host does not pretend it can start one.
 * return: an absolute path to the plugins directory, or "".
 */
func PluginsDir() string {
	var roots []string
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		roots = append(roots, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		roots = append(roots, wd)
	}
	for _, root := range roots {
		cand := filepath.Join(root, "plugins")
		if st, err := os.Stat(filepath.Join(cand, "host.py")); err == nil && !st.IsDir() {
			return cand
		}
	}
	return ""
}

// Activate registers the capabilities of every plugin named in `want` that is
// compiled in. It returns the tools to add to the agent registry, the plugin
// names actually switched on, and any requested-but-not-compiled-in names so the
// caller can warn the operator (they asked for a plugin this binary wasn't built
// with). Seams register as a side effect through each plugin's Host.
func Activate(want []string, d Deps) (active []toolapi.Tool, on, missing []string) {
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
		active = append(active, h.tools...)
		on = append(on, name)
	}
	return active, on, missing
}

// activation is the concrete Host used during Activate: it accumulates the tools
// a plugin adds and delegates seam registration to the core agent/tools package.
type activation struct {
	deps  Deps
	tools []toolapi.Tool
}

func (a *activation) Workspace() string      { return a.deps.Workspace }
func (a *activation) AddTool(t toolapi.Tool) { a.tools = append(a.tools, t) }

// AddSkill hands the card to whatever the caller wired in. Deps carries a
// function rather than this package importing the agent, which would be a cycle.
// Unwired, a skill is dropped and said to be dropped — the quiet version of that
// is what this method exists to end.
func (a *activation) AddSkill(name, markdown string) {
	if a.deps.AddSkill == nil {
		log.Printf("[plugins] %s ships a skill (%d bytes) and nothing is wired to receive it", name, len(markdown))
		return
	}
	a.deps.AddSkill(name, markdown)
}

func (a *activation) RegisterBinaryDecoder(mime string, fn func([]byte) (string, error)) {
	toolapi.RegisterBinaryDecoder(mime, fn)
}

func (a *activation) RegisterReaderFallback(fn func(ctx context.Context, rawURL string) (string, error)) {
	toolapi.RegisterReaderFallback(fn)
}
