package tools

import (
	"fmt"
	"sort"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/toolapi"
	"github.com/Compdeep/kaiju/internal/plugins"
)

// Registering kaiju's tools, and taking one of their names.
//
// "Core" is one word for three different things, and it is worth separating
// them before reading the list below.
//
//	REQUIRED   bash, service — and agent.GraftedToolNames() is the list, held
//	           to the scheduler's own source by a test rather than to this
//	           comment. The scheduler grafts nodes with these names: an exec
//	           step after a compute run, a health check after a build. Without
//	           them those steps fail at dispatch with "unknown tool", inside a
//	           run, long after registration.
//	EXPECTED   file_read, file_write, file_list, process_list, sysinfo,
//	           net_info, web_fetch, web_search. Nothing breaks without them,
//	           but a planner with no way to read a file or search the web is a
//	           planner that will say it cannot do things.
//	SHIPPED    clipboard, git, archive, office_extract, panel_push, env_list,
//	           disk_usage, process_kill, web_research, the memory three, the
//	           plugin three. Useful, and no more part of this engine's contract
//	           than any tool of your own.
//
// Register puts all three groups in. An application that wants less says so
// with Exclude — a headless service has no use for clipboard, and a planner
// shown a tool it can never sensibly call is a planner making worse plans.
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

	// Fetch bounds what web_fetch may spend on one page — disk, and the model.
	// The zero value is this package's own, which is what a caller that has not
	// thought about it should get; see FetchLimits.
	Fetch FetchLimits

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

	// Exclude names tools to leave unregistered, so an application can take one
	// of their names for itself. An entry matching no tool is reported rather
	// than ignored: a misspelt exclusion otherwise registers the tool it meant
	// to leave out, and the application finds out one line later as a name
	// collision, which points at the wrong mistake.
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
func Register(reg *toolapi.Registry, d Deps) ([]string, error) {
	skip := map[string]bool{}
	for _, name := range d.Exclude {
		skip[name] = true
	}
	unused := map[string]bool{}
	for name := range skip {
		unused[name] = true
	}

	var registered []string
	var failed error
	put := func(t toolapi.Tool) {
		if failed != nil {
			return
		}
		delete(unused, t.Name())
		if skip[t.Name()] {
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
	put(NewFileWrite(ConfineToWorkspace(d.Workspace)))
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
	// The workspace is where a fetched page is kept, which is what lets a later
	// step read the whole document rather than the part that fitted in a result.
	put(NewWebFetchIn(d.Workspace, d.Executor, d.Fetch))
	// The same instance web_research uses, so the two share one rate limiter.
	put(sharedSearch(d.Search))
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

	if failed != nil {
		return registered, failed
	}
	if len(unused) > 0 {
		names := make([]string, 0, len(unused))
		for name := range unused {
			names = append(names, name)
		}
		sort.Strings(names)
		return registered, fmt.Errorf("core.Register: excluded %v, which name no core tool — "+
			"the tools they were meant to leave out are registered", names)
	}
	return registered, nil
}

// FetchLimits bounds one fetch: how much of a page may be kept, and how much
// may be spent reading it.
//
// Neither is a property of the model, which is why neither is read from one.
// How much disk a page may take is the deployment's business — a host with a
// small disk and one building a corpus want different answers. How much may be
// spent extracting from a page is a cost decision, and cost is not something a
// tool should decide on a caller's behalf.
//
// Everything else about a fetch — how large a piece the extractor is given, how
// long a reply it may write — comes from the model that will read it, through
// llm.Client.WindowFor. There is no other number.
type FetchLimits struct {
	// MaxBodyBytes is the most of one page that is written to disk. A larger
	// page is written up to this and the result says it was cut, so a caller is
	// never silently given part of a document believing it has all of it.
	//
	// Zero means DefaultMaxBodyBytes.
	MaxBodyBytes int

	// ExtractTokenBudget is the most that may be spent reading one page, in
	// input tokens across every chunk. The number of chunks read is this
	// divided by what the model's window allows, so a large-window model reads
	// a long page in one pass and a small one in several — without either
	// number being written down here.
	//
	// Zero means DefaultExtractTokenBudget.
	ExtractTokenBudget int
}

// What a caller that has not said gets. Both are deliberately generous: the
// failure they exist to prevent is a run that silently saw a fraction of a
// document, and that is worse than a run that cost more than it had to.
const (
	// DefaultMaxBodyBytes is eight megabytes, which holds essentially any
	// document a fetch will meet while still being a number rather than "all
	// of the disk".
	DefaultMaxBodyBytes = 8 << 20

	// DefaultExtractTokenBudget is enough to read a large document in several
	// passes on a small-window model, and in one on a large one.
	DefaultExtractTokenBudget = 200_000
)

// resolve fills the zero values, so every caller of these reads one number.
func (f FetchLimits) resolve() FetchLimits {
	if f.MaxBodyBytes <= 0 {
		f.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if f.ExtractTokenBudget <= 0 {
		f.ExtractTokenBudget = DefaultExtractTokenBudget
	}
	return f
}
