# Embedding Kaiju: the core tools, and replacing one

For an application that imports this engine rather than running `kaiju` itself.
It covers what you must register, what you may leave out, and how to keep a tool
of your own under one of the engine's names.

`docs/tools.md` describes the `Tool` contract, the envelope and the catalogue.
This one is about the seam between your application and the set.

## Two calls

```go
import (
    "github.com/Compdeep/kaiju/agent/tools/core"
    mytools "github.com/example/myapp/internal/tools"
)

reg := ag.Registry()

// 1. The engine's core set, under the names the engine expects.
core.Register(reg, core.Deps{
    Workspace: cfg.Workspace,
    Memory:    ag.Memory(),
    Executor:  ag.ExecutorClient(),
})

// 2. Your own tools, and your replacements for any of the core set.
reg.Register(mytools.NewCheckAlerts(store))          // a name of your own
reg.Replace(mytools.NewProcessKill(store), "myapp")  // one of theirs, your behaviour
```

The order matters and only in one direction: `Register` replaces what is already
under a name, so calling it after your own registrations would overwrite them.
Core first, then yours.

## Why you must register the core set

The scheduler spawns nodes by tool name. When a `compute` run emits a plan, the
scheduler grafts an exec step with `ToolName: "bash"` and a health check with
`ToolName: "service"`. Those names are not configurable and should not be — they
are part of the engine's contract, the same as its node types.

An application that skips `core.Register` and supplies its own set under its own
names — `run_command` instead of `bash`, `service_control` instead of `service` —
will find every grafted step failing at dispatch with `unknown tool`. Nothing
else reports it. The plan is made, the graft is attempted, the node fails, and
the reason is a string in a log.

So: register the core set, then change what you need. Do not replace it with a
parallel set.

## What `Deps` is, and what happens when a field is empty

```go
type Deps struct {
    Workspace string        // sandbox root for file, bash, service, sysinfo, office
    Shell     string        // "sh" | "powershell" | "cmd"; empty picks the platform's
    Memory    *agent.Memory // nil omits memory_store, memory_recall, memory_search
    Executor  *llm.Client   // nil omits web_research; web_fetch still registers
    Search    core.SearchConfig
    Exclude   []string      // names to leave out
}
```

**An absent dependency omits the tools that need it.** It does not register a
tool that returns an error on every call. That rule is worth stating because the
alternative is tempting and wrong: a planner shown a tool whose every call fails
concludes the task is impossible, rather than that the capability is absent, and
it will report back that your system cannot do something it simply was not given.

`Register` returns the names it registered, so log them if you want a record of
what your planner is being shown.

## Replacing a core tool

The registry is a map from name to tool:

```go
func (r *Registry) Register(t Tool) error         // fails if the name is taken
func (r *Registry) Replace(t Tool, source string) // takes the name regardless
func (r *Registry) GetSource(name string) string  // which registration won
```

`Replace` is the override. Your implementation keeps the engine's name, so:

- the planner is shown one tool for the job, not two that overlap
- every graft that spawns that name resolves to your version
- the engine needs to know nothing about the substitution
- `GetSource` says which one is running, so a trace can tell

The `source` string is yours to choose. The core set registers as `"core"`.

## When to replace, and when to send it here instead

The question is who the behaviour is for.

**Send it to Kaiju** when any application embedding the engine would want it. A
`file_read` that can return the last N lines of a file rather than only the
first is not specific to anyone — that belongs here, and everyone gets it.

**Replace** when the behaviour is your product's. An endpoint-security product
whose `process_kill` requires a justification and links the kill to the alert
that caused it is not describing a gap in Kaiju; it is describing its own audit
trail. Pushing that into the engine would put one product's compliance model in
everyone's way. Keep the name, replace the tool.

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

## What is not in the core set

`plugin_list`, `plugin_enable` and `plugin_option` manage Kaiju's own plugin
host and need its configuration type. They are the application's to register.

`compute`, `edit_file`, `debug` and `image_read` are builtins of the `agent`
package rather than tools of this one, because they need the running graph, the
budget and the model clients. Register them the same way, gated on whatever your
application uses to decide they are wanted:

```go
if cfg.CodingEnabled {
    reg.Replace(agent.NewComputeTool(ag), "builtin")
    reg.Replace(agent.NewEditFileTool(ag), "builtin")
    reg.Replace(agent.NewDebugTool(ag), "builtin")
}
if cfg.VisionModel != "" {
    reg.Replace(agent.NewVisionTool(ag), "builtin")
}
```

The vision gate is the omission rule again: with no vision model configured,
`image_read` could only ever error, so it is not offered.
