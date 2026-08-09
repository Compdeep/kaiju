package core

import (
	"fmt"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/tools"
	"github.com/Compdeep/kaiju/internal/plugins"
)

// Registering the core tools, and replacing one of them.
//
// An application embedding this engine wants two things from this package and
// nothing else: give me the tools, and let me swap the one that does not suit
// me. Both are one call.
//
//	core.Register(reg, core.Deps{Workspace: ws, Memory: mem, Executor: client,
//		Exclude: []string{"process_kill"}})
//	reg.Register(mytools.NewProcessKill(store))
//
// The first call is the superset: everything this engine's scheduler and
// planner expect to exist, under the names they expect. The scheduler spawns
// nodes by name — "bash" for an exec step, "service" for a health check — so an
// application that skips this and registers its own set under its own names
// will find those steps failing at dispatch with "unknown tool", which is the
// single most common way an embedding goes wrong.
//
// The second call is the override, and Exclude is what makes it possible. A
// name is registered once and never reassigned: Register fails on a name
// already taken, so an application takes a name by not letting the core set
// have it. Your process_kill keeps the engine's name, so the planner sees one
// tool for the job and every graft still resolves, and there is no order in
// which the two calls silently produce a different result.
//
// The registry does have Replace, which takes a name regardless. It is for
// runtime activation — a plugin registering a tool into a running agent — and
// not for this. Startup registration is immutable on purpose: a swap that
// depends on call order is a swap nobody can see in a diff.
//
// Whether to override or to extend Kaiju is a judgement, and the line is: if
// the behaviour is useful to anyone embedding this engine, send it here; if it
// is your product's own — an audit trail, a clearance rule, a schema of your
// own — override.

// Deps are what the core tools need from the application.
//
// Every field is optional, and a missing one omits the tools that need it
// rather than registering a tool that can only fail. An application with no
// memory store gets no memory tools, and a planner is never shown a tool whose
// every call would return an error — which reads to a model as the task being
// impossible rather than the tool being absent.
type Deps struct {
	// Workspace is the directory the file, bash, service, sysinfo and
	// office tools treat as their sandbox. Empty means no sandbox: paths are
	// taken as given, which is what a host-management application wants and a
	// code-writing one does not.
	Workspace string

	// Shell is the interpreter bash runs commands through — "sh", "powershell",
	// "cmd". Empty picks the platform's own.
	Shell string

	// Memory is the store the three memory tools read and write. Nil omits
	// them.
	Memory *agent.Memory

	// Executor is the model web_fetch and web_research use to summarise what
	// they retrieved. Nil omits web_research entirely and leaves web_fetch
	// returning the page as it found it.
	Executor *llm.Client

	// Search configures the search provider and its rate limit. The zero value
	// is Startpage with DuckDuckGo as a fallback.
	Search SearchConfig

	// Plugins is the application's plugin configuration. Nil omits
	// plugin_enable and plugin_option, which have nothing to persist a change
	// to. plugin_list is registered whenever a plugin is compiled in, since
	// listing what is available needs no configuration.
	Plugins PluginConfig

	// AllowPluginActivation lets a run turn a plugin on for itself. It is the
	// application's policy, not a capability: an agent that may install its own
	// extensions mid-run is a different proposition from one that may not, so
	// this is off unless asked for.
	AllowPluginActivation bool

	// Exclude names tools to leave unregistered — the escape hatch for an
	// application that wants the set minus one, without listing the rest. A
	// name that is not a core tool is ignored, since the set changes between
	// versions and a stale exclusion should not be an error.
	Exclude []string
}

// Register puts the core tools in the registry.
//
// A name already taken is an error, not a replacement. If an application wants
// its own tool under one of these names it excludes that name here and
// registers its own — which means the substitution is visible in the call, and
// no order of calls can produce a set nobody intended.
//
// The names registered are returned so a caller can log or audit exactly what
// its planner will be shown. On an error nothing further is registered, and the
// names returned are the ones that were: registration stops at the collision
// rather than half-applying quietly.
func Register(reg *tools.Registry, d Deps) ([]string, error) {
	skip := map[string]bool{}
	for _, name := range d.Exclude {
		skip[name] = true
	}

	var registered []string
	var failed error
	put := func(t tools.Tool) {
		if failed != nil || skip[t.Name()] {
			return
		}
		if err := reg.RegisterWithSource(t, "core"); err != nil {
			failed = fmt.Errorf("core.Register: %w — exclude %q if this application "+
				"supplies its own", err, t.Name())
			return
		}
		registered = append(registered, t.Name())
	}

	// Shell and files. The sandbox is the workspace when there is one.
	put(NewBash(d.Shell, d.Workspace))
	put(NewFileRead(d.Workspace))
	put(NewFileWrite(d.Workspace))
	put(NewFileList(d.Workspace))
	put(NewArchive())
	put(NewOfficeExtract(d.Workspace))

	// The host.
	put(NewSysinfo(d.Workspace))
	put(NewProcessList())
	put(NewProcessKill())
	svc := NewService(d.Workspace)
	put(svc)
	put(NewNetInfo())
	put(NewEnvList())
	put(NewDiskUsage())
	put(NewClipboard())
	put(NewGit())
	put(NewPanelPush())

	// The web. web_fetch reads a page with or without a model; web_research
	// needs one to summarise across sources, so it is omitted without one.
	if d.Executor != nil {
		put(NewWebFetchWithLLM(d.Executor))
	} else {
		put(NewWebFetch())
	}
	put(NewWebSearchWithConfig(d.Search))
	if d.Executor != nil {
		put(NewWebResearch(d.Search, d.Executor))
	}

	// Memory, when there is somewhere to put it.
	if d.Memory != nil {
		put(NewMemoryStore(d.Memory))
		put(NewMemoryRecall(d.Memory))
		put(NewMemorySearch(d.Memory))
	}

	// Plugins. Listing what is compiled in needs nothing; turning one on needs
	// somewhere to record it and the application's permission.
	if len(plugins.Compiled()) > 0 {
		put(NewPluginList())
		if d.Plugins != nil && d.AllowPluginActivation {
			put(NewPluginEnable(reg, d.Plugins, svc))
			put(NewPluginOption(d.Plugins))
		}
	}

	return registered, failed
}
