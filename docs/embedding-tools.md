# Embedding Kaiju: registering its tools, and taking one of their names

For an application that imports this engine rather than running `kaiju` itself.
It covers what you must register, what you may leave out, and how to keep a tool
of your own under one of the engine's names.

`docs/tools.md` describes the `Tool` contract, the envelope and the catalogue.
This one is about the seam between your application and the set.

## Two calls

```go
import (
    "github.com/Compdeep/kaiju/tools"
    mytools "github.com/example/myapp/internal/tools"
)

reg := ag.Registry()

// 1. The engine's core set, minus any name you intend to take yourself.
names, err := tools.Register(reg, tools.Deps{
    Workspace: cfg.Workspace,
    Memory:    ag.Memory(),
    Executor:  ag.ExecutorClient(),
    Exclude:   []string{"process_kill"},
})

// 2. Your own tools, including the one under the name you excluded.
reg.Register(mytools.NewCheckAlerts(store))   // a name of your own
reg.Register(mytools.NewProcessKill(store))   // one of theirs, your behaviour
```

**A name is registered once and never reassigned.** `Register` fails on a name
already taken, in either direction, so there is no order of calls that silently
produces a set nobody intended. Taking one of the engine's names means excluding
it from the core set — which puts the substitution in the call that made it,
where a reader and a diff can both see it.

The registry does have `Replace`, which takes a name regardless. It exists for
runtime activation — a plugin registering a tool into a running agent — and is
not the way to override at startup.

## Three groups, one word

`tools` is one package and it holds three different kinds of thing. Knowing
which is which tells you what you may drop.

| | | |
|---|---|---|
| **required** | `bash`, `service` | the scheduler grafts nodes with these names; without them those steps fail at dispatch |
| **expected** | `file_read`, `file_write`, `file_list`, `process_list`, `sysinfo`, `net_info`, `web_fetch`, `web_search` | nothing breaks, but a planner that cannot read a file or search the web will say it cannot do things |
| **shipped** | `clipboard`, `git`, `archive`, `office_extract`, `panel_push`, `env_list`, `disk_usage`, `process_kill`, `web_research`, the memory three, the plugin three | useful, and no more part of the engine's contract than a tool of your own |

`Register` puts all three in. Drop what you do not want with `Exclude` — a
headless service has no use for a clipboard tool, and a planner shown a tool it
can never sensibly call makes worse plans for it.

## Why the required ones must keep their names

The scheduler spawns nodes by tool name. When a `compute` run emits a plan, the
scheduler grafts an exec step with `ToolName: "bash"` and a health check with
`ToolName: "service"`. Those names are not configurable and should not be — they
are part of the engine's contract, the same as its node types.

An application that skips `core.Register` and supplies its own set under its own
names — `run_command` instead of `bash`, `service_control` instead of `service` —
will find every grafted step failing at dispatch with `unknown tool`. Nothing
else reports it. The plan is made, the graft is attempted, the node fails, and
the reason is a string in a log.

So: register kaiju's set, then change what you need. Do not supply a parallel
set under names of your own.

## What `Deps` is, and what happens when a field is empty

```go
type Deps struct {
    Workspace string        // sandbox root for file, bash, service, sysinfo, office
    Shell     string        // "sh" | "powershell" | "cmd"; empty picks the platform's
    Memory    *agent.Memory // nil omits memory_store, memory_recall, memory_search
    Executor  *llm.Client   // nil omits web_research; web_fetch still registers
    Search    tools.SearchConfig

    Plugins               tools.PluginConfig // nil omits plugin_enable, plugin_option
    AllowPluginActivation bool              // may a run turn a plugin on?

    Exclude []string // names to leave out, so you can take one yourself;
                     // an entry matching no tool is reported, not ignored
}
```

**An absent dependency omits the tools that need it.** It does not register a
tool that returns an error on every call. That rule is worth stating because the
alternative is tempting and wrong: a planner shown a tool whose every call fails
concludes the task is impossible, rather than that the capability is absent, and
it will report back that your system cannot do something it simply was not given.

`Register` returns the names it registered, so log them if you want a record of
what your planner is being shown.

## Taking one of the engine's names

Exclude it, then register your own:

```go
tools.Register(reg, tools.Deps{Exclude: []string{"process_kill"}})
reg.Register(mytools.NewProcessKill(store))
```

Your implementation keeps the engine's name, so:

- the planner is shown one tool for the job, not two that overlap
- every graft that spawns that name resolves to your version
- the engine needs to know nothing about the substitution
- `GetSource(name)` says which registration holds it, so a trace can tell

The core set registers as `"core"`.

## When to override, and when to send it here instead

The question is who the behaviour is for.

**Send it to Kaiju** when any application embedding the engine would want it. A
`file_read` that can return the last N lines of a file rather than only the
first is not specific to anyone — that belongs here, and everyone gets it.

**Override** when the behaviour is your product's. An endpoint-security product
whose `process_kill` requires a justification and links the kill to the alert
that caused it is not describing a gap in Kaiju; it is describing its own audit
trail. Pushing that into the engine would put one product's compliance model in
everyone's way. Exclude the name and register your own under it.

The line is not about size. It is about whether the next person embedding this
engine would be glad of it or puzzled by it.

## Tools of your own

Anything the engine has no equivalent for is an ordinary `Register` under a name
of your own. Two things to get right, both covered in `docs/tools.md`:

- **Return a `ToolMessage`** by implementing `ExecuteTyped`. A tool that returns
  a bare string has no outcome, so an absence and a result look alike to
  everything downstream, including the statement the answering stage is given
  about what the evidence covers.
- **Declare an `OutputSchema`** describing the fields you actually return. That
  is what the planner reads to write `${step.N.field}` references. A tool with
  no schema can be called and never chained; a schema naming a field you do not
  set is worse, because the reference passes plan-time validation and fails at
  fire time.

## Plugins

The plugin tools are part of the core set. `plugin_list` arrives whenever a
plugin is compiled in. `plugin_enable` and `plugin_option` need somewhere to
record a change and your permission to make one:

```go
tools.Register(reg, tools.Deps{
    Plugins:               myPluginConfig,  // implements tools.PluginConfig
    AllowPluginActivation: true,
})
```

`tools.PluginConfig` is six methods — which plugins are on, where a remote host
is, how to start it, where the workspace is, and how to persist the first two.
It is an interface so you do not have to adopt kaiju's configuration file to
use its plugins; kaiju's own `*config.Config` implements it.

`AllowPluginActivation` is policy, not capability. An agent that may install its
own extensions mid-run is a different proposition from one that may not, so it
is off unless asked for.

## What is not in the core set

`compute`, `edit_file`, `debug` and `image_read` are builtins of the `agent`
package rather than tools of this one, because they need the running graph, the
budget and the model clients. Register them the same way, gated on whatever your
application uses to decide they are wanted:

```go
if cfg.CodingEnabled {
    reg.Register(agent.NewComputeTool(ag))
    reg.Register(agent.NewEditFileTool(ag))
    reg.Register(agent.NewDebugTool(ag))
}
if cfg.VisionModel != "" {
    reg.Register(agent.NewVisionTool(ag))
}
```

The vision gate is the omission rule again: with no vision model configured,
`image_read` could only ever error, so it is not offered.
