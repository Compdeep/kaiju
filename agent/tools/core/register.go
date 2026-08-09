package core

import (
	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/tools"
)

// Registering the core tools, and replacing one of them.
//
// An application embedding this engine wants two things from this package and
// nothing else: give me the tools, and let me swap the one that does not suit
// me. Both are one call.
//
//	core.Register(reg, core.Deps{Workspace: ws, Memory: mem, Executor: client})
//	reg.Replace(mytools.NewProcessKill(store), "myapp")
//
// The first line is the superset: everything this engine's scheduler and
// planner expect to exist, under the names they expect. The scheduler spawns
// nodes by name — "bash" for an exec step, "service" for a health check — so an
// application that skips this and registers its own set under its own names
// will find those steps failing at dispatch with "unknown tool", which is the
// single most common way an embedding goes wrong.
//
// The second line is the override. The registry is a map from name to tool:
// Register refuses a name already taken, Replace takes it regardless. So an
// application keeps the engine's name and supplies its own behaviour — a
// process_kill that records who asked and why, a bash that refuses outside a
// jail. The planner still sees one tool for the job, every graft still
// resolves, and the engine needs to know nothing about the substitution.
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

	// Exclude names tools to leave unregistered — the escape hatch for an
	// application that wants the set minus one, without listing the rest. A
	// name that is not a core tool is ignored, since the set changes between
	// versions and a stale exclusion should not be an error.
	Exclude []string
}

// Register puts the core tools in the registry, replacing any tool already
// registered under the same name.
//
// Replacing rather than refusing is deliberate: an application that registers
// its own tools first and then calls this would otherwise get a silent mix of
// the two, depending on the order it happened to call them in. Register the
// core set first, then Replace what you want to change — that order reads the
// same way it behaves.
//
// The names registered are returned so a caller can log or audit exactly what
// its planner will be shown.
func Register(reg *tools.Registry, d Deps) []string {
	skip := map[string]bool{}
	for _, name := range d.Exclude {
		skip[name] = true
	}

	var registered []string
	put := func(t tools.Tool) {
		if skip[t.Name()] {
			return
		}
		reg.Replace(t, "core")
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
	put(NewService(d.Workspace))
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

	return registered
}
