# Built-in Tools

Every capability the planner can call — a shell command, a web fetch, a file
edit — is a `Tool`. The Executive picks tools by name, the Dispatcher fires them,
and the IGX gate decides whether each invocation is allowed. This doc describes
the tool contract, the registry that holds them, the envelope they emit, and the
full catalogue of what ships built in.

## The Tool interface

`internal/agent/tools/skill.go`. Four methods, implemented by every compiled
built-in and every SKILL.md wrapper:

```go
type Tool interface {
    Name() string                                              // registry key + LLM function name
    Description() string                                       // the text the LLM plans against
    Parameters() json.RawMessage                               // JSON Schema for the call args
    Impact(params map[string]any) int                          // IGX tier for THIS invocation
    Execute(ctx context.Context, params map[string]any) (string, error)
}
```

### Impact is per-invocation

`Impact` is evaluated against the **actual params of the call**, not the tool in
the abstract. `bash("ls")` returns `ImpactObserve` even though a `bash("rm -rf
/")` on the same tool returns `ImpactControl` — the gate sees the real
side-effect of *this* command, not a pin on the whole tool. This is the whole
point of passing `params` into `Impact`: a tool that can do dangerous things is
still cheap to gate when it isn't doing them.

The three tiers are the same integer scale as the intent registry's builtin
ranks, so the gate compares tool impact and intent rank directly:

| Const | Value | Meaning |
|---|---|---|
| `ImpactObserve` | `0` | read-only, no side effects |
| `ImpactAffect` | `100` | reversible side effects |
| `ImpactControl` | `200` | irreversible / destructive |

These ranks are locked by invariant (`UpdateIntent` rejects rank changes on
builtins), so tool authors hardcode them. The gate passes an invocation when
`impact <= min(intent, clearance, scope_cap)`; `impact == 0` always passes. See
`docs/intents.md` and `docs/authorization.md`.

## The Registry

`internal/agent/tools/registry.go`. One thread-safe, in-process map keyed by tool
name. Each entry carries the tool plus two pieces of metadata:

- **source** — where it came from: `"builtin"`, `"skillmd:<path>"` (a SKILL.md
  file with command-dispatch), or `"plugin"` (a compiled or remote plugin tool).
- **enabled** — a dashboard-toggleable flag. `Get(name)` returns a tool *only*
  when it exists **and** is enabled, so a disabled tool vanishes from planning
  without being unregistered.

`Register` / `RegisterWithSource` refuse a name collision; `Replace` swaps
atomically (used by the SKILL.md hot-reload watcher and by runtime plugin
activation). `AllToolDefs` / `ToolDefsForNames` render the enabled set into
OpenAI function-calling defs for the LLM.

## The ToolMessage envelope

`internal/agent/tools/toolmessage.go`. Every tool result is a uniform envelope so
an edge can frame presence / absence / failure the same way regardless of which
tool ran. The envelope adds only the framing signals; the tool's own payload
lives verbatim in `Data`.

```go
type ToolMessage struct {
    Kind    string          // payload discriminator: page|file|listing|command|kv|status|text (search too)
    Status  ToolStatus      // ok | empty | error
    Content string          // renderable evidence text
    Detail  string          // why: the note when empty, the reason when error
    Data    json.RawMessage // the tool's own payload, verbatim + field-addressable
}
```

**`empty` is first-class.** A tool that ran fine but found nothing reports
`StatusEmpty` with a `Detail` saying why — absence is a real result, distinct
from `error` (the tool failed) and from `ok` (usable content). An edge reads the
empty envelope and does not fabricate; a web_fetch that pulled a document with no
text layer says so rather than inventing content.

Constructors: `ToolOK(kind, content, data)`, `ToolEmpty(kind, detail)`,
`ToolFail(kind, detail, data)`, `ToolText(content)`. `Data` is **field-addressable**
— the Dispatcher resolves `${step.N.field}` placeholders against it (see
`docs/graph.md`, "DI and validation"). `ParseToolMessage` reconstructs an
envelope from a result string and returns `ok=false` for raw/legacy output, so a
non-envelope is never mistaken for one.

## Optional interfaces

A tool can implement any of these to opt into extra behaviour. The registry
probes each via a type assertion (`GetOutputSchema`, `GetThrottle`,
`GetDisplayHint`); a tool that doesn't implement one simply doesn't get that
behaviour.

| Interface | Method | What it buys |
|---|---|---|
| `Outputter` | `OutputSchema() json.RawMessage` | Declares the shape of `Data`. This is what makes `${step.N.field}` wiring **checkable** — the Executive wires placeholders against the real result shape and the Dispatcher validates the field path. `EnvelopeSchema(dataSchema)` builds the schema so it can't drift from the actual envelope. |
| `TypedExecutor` | `ExecuteTyped(...) (ToolMessage, error)` | The typed output path: return a `ToolMessage` directly instead of a marshalled string, so the Dispatcher stores the typed body with no JSON round-trip. Tools we control implement this; opaque tools (plugins, skill-md) stay on plain `Execute` and are wrapped as text. |
| `Throttled` | `Throttle() time.Duration` | A minimum interval between consecutive invocations; the scheduler enforces it per-tool (rate-limits external APIs). |
| `Displayer` | `DisplayHint(params, result) *DisplayHint` | After `Execute`, the scheduler emits a `panel_show` SSE event so the frontend renders the output in a composable panel (code, file, inline HTML/SVG). |
| `ToolMeta` | `Source()`, `IsUserInvocable()` | Enriched origin + slash-command invocability. Only SKILL.md wrappers implement it; builtins don't need to. |

## Catalogue

Registered in `cmd/kaiju/main.go` (~L389–510). Static-impact tools are grouped by
their fixed tier; the four dynamic tools whose impact depends on params are listed
separately.

### Observe (`ImpactObserve`)

| Tool | Purpose |
|---|---|
| `file_read` | Read a file's contents as text. |
| `file_list` | List files and directories at a path. |
| `web_search` | Search the web; returns titles, URLs, snippets. |
| `web_fetch` | Fetch a URL and extract its content (markdown / text / raw / summary). |
| `web_research` | Search **and** read the top results in one step — every source is grounded, so the planner never invents a URL or stops at snippets. Preferred research path. |
| `office_extract` | Extract text from a Word/PowerPoint/Excel (`.docx`/`.pptx`/`.xlsx`) file. See `docs/uploads-extraction.md`. |
| `net_info` | List network interfaces / IPs, or check connectivity to a host. |
| `disk_usage` | Disk space for mounted filesystems, or directory size for a path. |
| `env_list` | List / search environment variables (sensitive values masked). |
| `process_list` | List running processes with PID, CPU, memory. |
| `sysinfo` | Hostname, OS, arch, working dir, current time. |
| `panel_push` | Push generated HTML/SVG/diagrams/code to the composable panel (no file). |
| `image_read` | Answer a question about an image file via the vision model — reads charts, screenshots, scanned pages mid-task. *(config-gated: only registered when `vision.model` is set.)* |
| `memory_store` | Store a key/value in persistent memory with optional TTL + tags. *(config-gated: memory enabled.)* |
| `memory_recall` | Recall a value by key. *(config-gated: memory enabled.)* |
| `memory_search` | Search memory entries by tag. *(config-gated: memory enabled.)* |
| `plugin_list` | Report which optional plugins are built in and which are active. *(config-gated: at least one plugin compiled in.)* |

`memory_store` is `Observe`, not `Affect` — writing to kaiju's own internal KV is
not a side effect on the user's world, and memory is deliberately confined to the
chat boundary (see `docs/memory.md`).

### Affect (`ImpactAffect`)

| Tool | Purpose |
|---|---|
| `file_write` | Byte-writer: write `content` to `path`. No LLM — use when the exact bytes are in hand. |
| `compute` | Generate a VALUE via a runnable script (shallow) or scaffold a project (deep); shallow captures stdout on `.output`. *(config-gated: `tools.compute.enabled && !agent.disable_coding`.)* |
| `edit_file` | Edit/create a known file via the Coder LLM; `task_files` required. *(same coding gate.)* |
| `debug` | The REPAIR super-tool — grafts Holmes RCA → microplanner fix → validators. Write-capable, so gated like `compute`. *(same coding gate.)* See `docs/graph.md`. |
| `archive` | Create / extract / list zip and tar.gz archives. |
| `plugin_enable` | Switch on a built-in-but-off plugin, making its tools live immediately. *(config-gated: `allow_runtime_plugin_activation`.)* Audited. |
| `plugin_option` | Set and persist a plugin config option (e.g. the remote plugin host URL). *(config-gated: `allow_runtime_plugin_activation`.)* |

`compute`, `edit_file`, and `debug` are agent-bound — they drive LLM lanes, so
they are constructed with the `*Agent` and dispatched via `ExecuteWithContext`
rather than plain `Execute`. `debug`'s impact is `Affect` because its
microplanner fix edits files.

### Control (`ImpactControl`)

| Tool | Purpose |
|---|---|
| `process_kill` | Terminate a process by PID (destructive). |

Only `process_kill` is *statically* Control. The other paths that can reach
Control are the dynamic tools below.

### Dynamic (impact depends on params)

Because `Impact(params)` is per-invocation, four tools return a different tier per
call. The gate sees the real tier for the actual arguments.

| Tool | Observe | Affect | Control |
|---|---|---|---|
| `bash` | read-only command | write command; destructive command scoped **inside** the workspace sandbox | destructive command (`rm`, `kill`, …) touching paths outside the workspace |
| `git` | `status`, `log`, `diff`, `show`, `branch_list` | `add`, `commit`, `checkout`, `branch_create`, `stash`, `tag` | `push`, `pull`, `reset`, `merge`, `rebase` |
| `service` | `status`, `logs`, `list` | `start`, `stop`, `restart`, `remove` | — |
| `clipboard` | `read` | `write` | — |

`bash`'s destructive-but-workspace-only downgrade (`isWorkspaceOnly`) is why a
coding run can `rm -rf` inside its own sandbox at the `operate` intent without
requiring `override` — the blast radius is the agent's scratch space, not the
host.

### Config gates, in one place

| Tool(s) | Registered only when |
|---|---|
| `compute`, `edit_file`, `debug` | `tools.compute.enabled && !agent.disable_coding` |
| `image_read` | `vision.model != ""` |
| `memory_store` / `memory_recall` / `memory_search` | memory subsystem enabled |
| `plugin_list` | ≥ 1 plugin compiled in |
| `plugin_enable`, `plugin_option` | additionally `allow_runtime_plugin_activation` |
| `bash` / `file_*` / `web_*` / `sysinfo` | the matching `tools.*.enabled` flag |

Everything else (`process_list`, `process_kill`, `service`, `office_extract`,
`net_info`, `env_list`, `disk_usage`, `clipboard`, `archive`, `git`,
`panel_push`) is always registered.

## Plugin tools

Compiled (build-tag) and remote (out-of-process) plugins register into this same
registry with source `"plugin"`, so the planner can't tell `pdf_extract`
(compiled) from a remote reader tool — they're all just `Tool`s. Plugins also
install the two `web_fetch` enrichment seams (a binary decoder and a reader
fallback). See `plugins.md` for the framework and `docs/uploads-extraction.md`
for how those seams feed `web_fetch`.

## Relevant source

| file | responsibility |
|---|---|
| `internal/agent/tools/skill.go` | `Tool` interface, impact tiers, optional interfaces |
| `internal/agent/tools/registry.go` | the in-process registry (source + enabled) |
| `internal/agent/tools/toolmessage.go` | the `ToolMessage` envelope + constructors |
| `internal/agent/tools/decoders.go` | `web_fetch` binary-decoder + reader-fallback seams |
| `internal/tools/*.go` | the built-in tool implementations |
| `internal/agent/builtin_compute.go` / `builtin_edit_file.go` / `builtin_debug.go` / `builtin_vision.go` | the agent-bound tools |
| `cmd/kaiju/main.go` (~L389–510) | registration + config gates |
