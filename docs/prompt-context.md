# Prompt Context System

Every LLM-calling node in the agent fetches its prompt context through **ContextGate**, the singleton context API attached to each investigation graph. ContextGate is the single entry point for all context loading, replacing earlier scattered direct disk reads. There is exactly one ContextGate per investigation, constructed at graph setup and reachable as `graph.Context`.

## Why a single API

Before ContextGate, each LLM caller built its prompt by hand: the executive read the workspace tree directly, the architect scanned files inline, the debugger loaded blueprints from disk. Three problems:

1. **Cross-session leakage** — every direct read pulled the latest blueprint or worklog from disk regardless of which session was active. Asking about the weather could surface a webapp blueprint from yesterday's project.
2. **No central audit** — there was no way to inspect "what context did this LLM call actually receive?" without grepping through six files.
3. **Each new node had to reinvent the wheel** — adding a new caller meant copying the disk-read patterns and hoping you didn't introduce a leak.

ContextGate fixes all three. Every LLM caller declares what it needs as a typed request, and the gate loads it through registered sources. Adding a new node is one `gate.Get()` call. Auditing is `KAIJU_PROMPT_DEBUG=1` and reading the dump.

## The API

```go
type ContextRequest struct {
    Query         string       // optional. If set, the curator runs.
    QuerySources  []SourceSpec // sources curator reads to build the summary
    ReturnSources []SourceSpec // sources returned verbatim, never curated
    MaxBudget     int          // total char budget (default 16000)
}

type ContextResponse struct {
    Summary string             // curator output (empty if no Query)
    Sources map[string]string  // verbatim sources by name
}
```

Two execution paths:

- **Deterministic** (no Query): load ReturnSources, return verbatim. No LLM call. This is what 5 of 6 nodes use.
- **Curated** (with Query): load QuerySources, run a small executor LLM call to extract relevant slices, return as Summary. Used by the debugger so its problem statement filters out irrelevant noise from a large worklog.

## The 10 sources

| Source | What it returns | Typical caller |
|---|---|---|
| `blueprint` | Latest blueprint markdown for the session | executive, debugger |
| `worklog` | Last N lines of the worklog (filterable) | reflector, aggregator, observer, executive, compute |
| `node_returns` | Resolved/failed node results from this graph | reflector, aggregator |
| `workspace_tree` | Light file tree scan | executive |
| `workspace_deep` | Deep workspace scan including small file contents | compute architect |
| `function_map` | Discovered function declarations across the workspace | compute architect |
| `existing_blueprints` | All blueprints in the session subdir | compute architect |
| `service_state` | Long-running process registry | (not yet wired in production) |
| `history` | Recent conversation turns from `Trigger.History` | (not yet wired) |
| `skill_guidance` | Topic-filtered guidance from active skill cards | debugger, architect, coder, aggregator |

Each source has typed helper constructors (`Blueprint()`, `BlueprintSection("Files")`, `Worklog(20, "all")`, `NodeReturns("failures")`, `WorkspaceTreeFocus(3, "src")`, etc.) so call sites read declaratively. New sources are added by registering an implementation in `registerDefaultSources()` and a constructor next to the existing ones.

## What's intentionally NOT in ContextGate

### Memory

There is no `memory` source. **This is a security boundary, not an oversight.**

Conversational memory (semantic facts, episodic experiences, procedural knowledge about the user) lives at the **chat boundary**, not in the execution layer. Memory is loaded once at the chat input (`internal/api/api.go handleChat`) and prepended to `Trigger.History` as a system message. From the agent's point of view, memory looks like opaque conversation history — execution-layer code can read `trigger.History` but it never distinguishes "real history" from "injected memory."

#### Why this matters (anti-prompt-injection)

The execution layer runs untrusted tool output through reasoning steps: bash command output, web fetches, compute/coder LLM responses, debugger plans. Any of those can contain adversarial content trying to manipulate the agent. If memory were reachable from execution-layer code, a malicious tool result could:

- Exfiltrate stored facts by causing them to be quoted in a subsequent LLM call.
- Rewrite the user's memory by inducing the agent to call a hidden memory write path.

By keeping memory at the chat boundary, both reads and writes are attested by the authenticated user request itself. There is no code path from inside the execution layer to the memory package.

The one exception is the explicit memory tools (`memory_store`, `memory_recall`, `memory_search`). Those let the LLM **deliberately** call memory as a tool action — auditable in the worklog and requiring explicit LLM intent. That's allowed because it requires active LLM decision-making, not automatic injection by code processing untrusted input.

If you want to add a new memory access path: don't add it to ContextGate. Add it to the chat boundary in `api.go` where it can be attested by the authenticated user request.

### Tool execution state

Tools (file_read, bash, service, etc.) write their results back into the graph as node results. The next LLM call sees those via the `node_returns` source. ContextGate doesn't reach into running tools or speculate about pending results.

### The kernel heartbeat

`internal/agent/kernel.go` reads the worklog directly (not via the gate) because it's a watchdog goroutine that runs across investigations and doesn't feed an LLM. It observes log lines for stuck-detection patterns. This is at a different layer than prompt assembly.

### Path resolution operations

`scheduler.go` injects blueprint paths (not content) into child node params via `latestBlueprintPath`. ContextGate returns content, not paths. Path resolution is a metadata operation outside the gate's scope.

## Session scoping

Two pieces of state used to live as global files and were a cross-session leak vector:

- **Worklog**: now at `<workspace>/sessions/<sessionID>/worklog`. Empty `sessionID` falls back to legacy `<workspace>/.worklog` for backwards compatibility.
- **Blueprints**: now at `<workspace>/blueprints/<sessionID>/`. Same fallback.

Path helpers `worklogPath(workspace, sessionID)` and `blueprintsDir(workspace, sessionID)` are the only places that resolve session paths. Every caller (read, write, contextgate source) goes through these.

## Debugging context

Set `KAIJU_PROMPT_DEBUG=1` and every `ContextGate.Get()` call dumps its full request and response to `/tmp/kaiju-prompts/ctxgate_<timestamp>.txt`. Per-call logging is always on:

```
[ctx] gate.Get query="" query_sources=[] return_sources=[blueprint worklog] budget=16000 return_size=2483 summary_size=0 curator=false took=312µs
```

If you ever wonder "what did this LLM call actually receive?", the answer is in those logs and dumps. There is no other path.

## Adding a new source

1. Add a constant to the `Source*` block at the top of `contextgate.go`.
2. Register the source in `registerDefaultSources()`.
3. Implement a `ContextSource` (a struct with `Name()` and `Load()` methods).
4. Add typed helper constructors next to the others (`Foo()`, `FooFiltered(...)`).
5. Update the per-source filter convention doc block in `contextgate.go`.
6. Add tests in `contextgate_test.go`.
7. If the parsing logic is reusable (e.g. markdown sections), put the parser in `textutil.go`.

Don't add a memory source. Re-read the security boundary section above if you're tempted.
