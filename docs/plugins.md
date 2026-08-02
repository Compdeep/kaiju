# Plugins

Kaiju ships a small default binary and grows capabilities two ways. Both are
optional, both are off unless switched on, and both converge on the **same tool
registry** — the planner sees one flat catalogue and cannot tell a compiled tool
from a remote one.

```
                    ┌──────────────────────────────┐
   COMPILED         │        Tool registry         │        REMOTE
   (in-process,     │  (source "plugin")           │   (out-of-process,
    build-tag)      │                              │    Python host)
                    │   web_fetch  ← seams →        │
  pdf_extract ──────┼──► pdf_extract               │
  (application/pdf  │    web_read  ◄───────────────┼──── web_read
   decoder seam)    │    (reader seam)             │     (reader: true)
                    └──────────────────────────────┘
        │                                                    │
   Register(Host)                                    GET /plugins manifest
   at Activate                                       → one synth tool per entry
```

- **Compiled plugins** are Go, linked into the binary only behind a build tag
  (`-tags plugin_pdf`), for capabilities that need kaiju's internals or the hot
  path. Heavy or niche dependencies stay out of the default build.
- **Remote plugins** live in an out-of-process host (any language; the reference
  host is Python), reached over REST. For the ecosystem — headless rendering, ML
  models, data libraries — and for isolation.

A remote plugin needs **no kaiju rebuild**: the bridge fetches the host's
manifest and synthesizes one kaiju tool per advertised tool at runtime. To the
planner, `pdf_extract` (compiled) and `web_read` (remote) are indistinguishable —
both are registered under source `"plugin"`.

## Compiled framework

`internal/plugins/registry.go`.

A plugin adds itself from an `init()` in a **build-tagged** file, so the default
build links neither the plugin nor its dependencies:

```go
//go:build plugin_pdf

func init() { plugins.Add(plugin{}) }
```

`Add` only records the plugin (`registered` map). Nothing runs until `Activate`,
which is called at boot for every name in config `plugins` / `--plugins` that is
compiled in.

### Host

`Host` is the only surface a plugin touches at registration — never global state,
so a plugin's contribution is explicit at one call site and capturable in a test.
Two kinds of capability:

| Method | Kind | Effect |
|--------|------|--------|
| `AddTool(Tool)` | **tool** | a tool the planner calls by name |
| `RegisterBinaryDecoder(mime, fn)` | **seam** | teach core `web_fetch` to turn a typed body (e.g. `application/pdf`) into text |
| `RegisterReaderFallback(fn)` | **seam** | teach core `web_fetch` a heavier re-read path (render + extract) for a URL with no extractable content |
| `Workspace()` | — | the sandbox root file-touching tools resolve paths under |

A **tool** is planner-callable. A **seam** is invoked by the core tool it
enriches, not by the planner — enabling the pdf plugin means `web_fetch` can now
read a PDF a search turned up, without the planner choosing anything. Grow the
interface as new seams appear (a skill registrar, a renderer, …); a plugin uses
only the methods it needs.

### Plugin

```go
type Plugin interface {
    Name() string        // activation key in config `plugins` / --plugins
    Description() string  // one line, surfaced by plugin_list
    Register(Host)        // contribute capabilities; called once at activation
}
```

### Activate

```go
func Activate(want []string, d Deps) (active []Tool, on, missing []string)
```

For each compiled-in name in `want`, `Activate` builds an `activation` Host, calls
the plugin's `Register`, marks it active, and collects the tools it added. Seams
register as a side effect through the Host. `missing` names were requested but not
compiled in, so the caller can warn the operator (they asked for a plugin this
binary wasn't built with). `Deps` carries the shared services a Host is built from
(today just `Workspace`; extend as plugins need more).

The read model behind `plugin_list` is `Catalog()` (`Info{Name, Description,
Active}`), plus `Compiled()`, `Get(name)`, `IsActive(name)`, `MarkActive(name)`.

### Why build tags

The point of the tag is to keep a dependency out of the **default** binary. The
pdf plugin links `github.com/ledongthuc/pdf`; a default build never pulls it. An
operator opts in at compile time (`-tags plugin_pdf`) and again at runtime (config
`plugins: ["pdf"]`) — two switches, no fork. A name in config that the binary
wasn't built with is reported as missing and ignored, never a boot failure.

## The pdf plugin

`internal/plugins/pdf/pdf.go`, build tag `plugin_pdf`. One `Register` contributes
both capability kinds at a single call site:

```go
func (plugin) Register(h plugins.Host) {
    h.AddTool(&extractTool{workspace: h.Workspace()})       // TOOL
    h.RegisterBinaryDecoder("application/pdf", decodeBytes)  // SEAM
}
```

- **`pdf_extract` tool** — reads a PDF file's text layer to plain text (input is a
  file path, sandboxed to the workspace like `file_read`). Impact `observe`.
- **`application/pdf` decoder seam** — core `web_fetch` runs it on a downloaded PDF
  body, so a PDF a search turns up (many government / academic primary sources are
  PDFs) reads like any other page. The decoder lives in the optional plugin; core
  holds only the seam.

Both paths share `extractText`, which recovers word spacing from glyph X-positions
(`GetTextByRow`) rather than `ledongthuc`'s run-concatenated `GetPlainText`. Scope
is **digital (text-layer) PDFs only** — a scanned / image-only PDF returns little
or no text and says so (that needs a vision model), never fabricates. Output is
capped at `defaultMaxChars` (200 000, raisable per call).

## Remote bridge

`internal/plugins/remote/remote.go`, build tag `plugin_remote`. A generic REST
bridge to a host that speaks the kaiju remote-plugin protocol. Its plugin name is
`remote`; enable it with `-tags plugin_remote` + config `plugins: ["remote"]`.

### Protocol

| Method | Path | Purpose |
|--------|------|---------|
| `GET`  | `/plugins` | The manifest: every plugin, its tools + JSON-schema params + optional skill. |
| `POST` | `/invoke/{tool}` | Run a tool with `{"params": {...}}`; returns a kaiju ToolMessage envelope. |
| `GET`  | `/health` | Liveness + loaded plugin names. |

A **ToolMessage envelope** is
`{"kind", "status": "ok"|"empty"|"error", "content"?, "detail"?, "data"?}`.

The host URL comes from `KAIJU_PLUGIN_HOST` (bridge default
`http://127.0.0.1:8091`); an optional `KAIJU_PLUGIN_TOKEN` is sent as a bearer
token on every call. The host may be written in any language — kaiju only speaks
the protocol.

### One synthesized tool per advertised tool

`Register` fetches the manifest (5 s timeout) and, for each advertised tool, adds
a `remoteTool` backed by a POST to `/invoke/{name}`. **Adding a plugin to the host
needs no kaiju rebuild** — it appears on the next manifest fetch. `Register` is
best-effort: a down host logs and contributes nothing (the agent runs without
those tools) rather than failing boot — the same graceful degradation kaiju
applies to any missing tool.

A manifest tool's `impact` string maps to the IGX ladder — `""`/`observe`/`read` →
Observe, `control`/`destroy`/`delete`/`irreversible` → Control, anything else →
Affect (unknown side effects treated as reversible-write).

### The reader seam

A manifest tool with `"reader": true` is **also** registered as `web_fetch`'s
`ReaderFallback` (`readerFrom`, remote.go:112). Once the plugin is enabled,
`web_fetch` reads every page through it automatically — JS/SPA pages included —
not only when the planner calls the tool by name. `readerFrom` invokes the tool
with `{"url": rawURL}` and returns the extracted text; on an `empty`/`error`
envelope it returns `""`, so `web_fetch` cleanly falls back to its built-in
extraction.

(A manifest may also carry a `skill` per plugin. It is fetched but **not yet
injected** — that needs a `Host.AddSkill` hook, a small follow-up; the tool works
without it today.)

### The 60 s invoke ceiling

`invokeHTTPClient` (remote.go:189) bounds every tool invocation at **60 s**. A host
that accepts the connection but then hangs — a stuck render, a crash-looping
worker — must never hang a `web_fetch` or the whole run. On timeout (or any
transport error, or a non-200), `Execute` returns a `ToolFail` envelope, not a
kaiju crash, and `web_fetch` falls back to its built-in reader. The response body
is read under a 16 MB `LimitReader`. This is a hard per-request ceiling on top of
the caller's ctx, whichever fires first.

## The Python host (reference)

`plugins/`. A long-running FastAPI gateway that loads the plugins in that
directory and exposes them over the protocol above.

### host.py — the gateway

`host.py` is a thin FastAPI app: `/health`, `/plugins`, `/invoke/{tool}` (404 on
an unknown tool). Plugins are loaded **once at startup** into a module-level
`REGISTRY` and stay **warm** for the life of the process — a plugin can hold warm
state (a Playwright browser pool, a loaded model, a connection pool) across many
calls. `_auth` enforces `KAIJU_PLUGIN_TOKEN` as a bearer when it is set, a no-op
otherwise.

### registry.py — per-service queues

`load_plugins` scans each subdirectory for a `plugin.py` (`MANIFEST` dict +
`invoke(tool, params)`, sync or async) and an optional `skill.md`, and imports it
once. Concurrency is **per plugin**:

- `_gate(tool)` returns the owning plugin's `asyncio.Semaphore` (created lazily so
  it binds to the running loop). **All tools of one plugin share one gate** — a
  per-service queue; different plugins have independent gates, so a busy/slow
  service never blocks another.
- A plugin's `max_concurrency` (manifest, default **1** = serialize) is the slot
  count. Requests beyond it await a slot instead of piling on.
- `invoke` runs the plugin's function inside the gate. A plugin crash becomes an
  `error` envelope, never a 500; a plugin that returns a bare string is wrapped as
  `ok` content.

### webreader — the reference plugin

`plugins/webreader/plugin.py`. One tool, `web_read`, `"reader": true`, impact
`observe`, `max_concurrency: 4` (each render is a browser context; the cap bounds
memory, overflow queues — and kaiju's 60 s ceiling caps the wait before it falls
back). Two tiers, cheap first:

- **Tier 1 — static.** `trafilatura` downloads + extracts the main article text.
  No browser. Runs in a threadpool so it stays off the event loop.
- **Tier 2 — rendered.** A **warm** headless Playwright/Chromium browser, launched
  once as a lazy singleton and reused across calls. `_get_browser` returns `None`
  when Playwright or the Chromium binary is missing, so tier 1 stands on its own
  rather than erroring.

Tier 2 fires only when the static read is thin (`_MIN_CHARS` 400) or `render=true`.
If both tiers come back thin, `web_read` returns an **empty** envelope reporting
the gap honestly (login-walled, image-only, blocked) — kaiju's coverage edge reads
that status rather than letting the answer fabricate.

### start.sh

`plugins/start.sh` bootstraps and runs the host: it creates the venv + installs
base deps on first run, then `exec`s uvicorn on `$1` (default **8092**). Idempotent
— enabling a plugin from chat runs it automatically when the host isn't already up,
so a user never starts a service by hand. The render tier is optional and heavy
(`requirements-render.txt` + `playwright install chromium`); without it webreader
runs static-only. On a small box, run the render tier on a separate machine and
point `KAIJU_PLUGIN_HOST` at it.

> The reference host runs on **8092**, not the bridge's env default 8091 — 8091 is
> the MCS worker's port. The `webreader` capability's `DefaultURL` and `start.sh`
> default both encode 8092.

## Supervision by kaiju's service manager

The Python host is a process kaiju spawns itself, so kaiju supervises it through
its **own** service manager (`internal/tools/service.go`), the same `service` tool
the planner uses for any long-running process — not systemd or pm2. (See
`docs/service.md` for the tool itself.)

`plugin_enable` brings a host up via `StartManaged(name, cmd, workdir, port)` —
`service start` with `auto_restart=true` — so the host is tracked, logged, and
health-checked like any other service:

- **10 s health loop** — `healthLoop` → `reapDead` every 10 s. `isAlive` (signal 0
  + a `/proc/<pid>/status` zombie check) clears the fast-crash counter; a dead
  service flagged `AutoRestart` is respawned.
- **freePort before every spawn** — `freePort` (`fuser -k {port}/tcp` + a 400 ms
  settle) runs before each start, scoped to the **exact** port so it can never
  touch another service (e.g. the MCS worker). One process can ever hold the port,
  so there is only ever **one instance** — this is what stops uvicorn from
  multiplying across restarts.
- **Port-skip** — a dead `AutoRestart` service whose `Port` is still served
  (`portOpen`) is **not** restarted: an orphan from a prior run holds it, and
  respawning would only crash-loop on a bind conflict. It is effectively up via
  whatever answers the port.
- **Crash-backoff** — a service that dies within `minUptime` (15 s) of starting
  increments a counter; at `maxFastCrashes` (5) kaiju gives up, marks it `crashed`,
  and clears `AutoRestart`, so a host that can't start (bad command, missing dep)
  never loops forever.

Detached spawn is `sh -c` under `Setsid`, logs truncated each start (validators
tail them for current-run errors). Records live in `<workspace>/.services.json`,
logs under `<workspace>/.services/`.

### Two-layer self-heal

`plugin_manage.go` never trusts a stale `"active"` flag — it verifies **real tools**
are registered:

- **`ensureRemoteUp`** (shared by chat-enable and boot self-heal) resolves the host
  URL, exports `KAIJU_PLUGIN_HOST`, and calls `bridge.Register` on a live host. If
  that registers no tools (host died after boot — active but toolless), it
  `StartManaged`s the host, `waitHostUp`s (polls `GET /plugins`, up to 30 s), then
  re-registers before marking active. An active-but-toolless state heals instead of
  falsely reporting success.
- **`EnsureRemoteHostsUp`** runs at boot: for every remote capability already in
  config (only when `plugins` lists `remote`), it runs `ensureRemoteUp`. Paired
  with the health loop's auto-restart, the reader "just works" across restarts and
  host crashes instead of silently coming up dead.

## Runtime tools

Registered in `main.go` only when at least one plugin is compiled in; the two
mutating ones only when the host set `AllowRuntimePluginActivation`.

| Tool | Impact | Purpose |
|------|--------|---------|
| `plugin_list` | observe | Lists compiled-in plugins (active / available) **plus** remote capabilities by name. Hides the `remote` bridge — it's plumbing, surfaced as its individual capabilities (`webreader`, …). |
| `plugin_enable` | affect | Switches on a compiled-but-off plugin at runtime and persists it. Compiled → `Register` on a live host; a remote capability → `enableRemote` (connect + supervise the host). Runs through the intent gate and is audited. |
| `plugin_option` | affect | Persists a plugin config option — today the remote host URL (`{"name":"remote","key":"host","value":"http://…"}`), then `plugin_enable {"name":"remote"}`. |

`plugin_enable`'s `liveHost` writes into the **running** registry
(`reg.Replace(t, "plugin")`), so a runtime-enabled tool appears on the planner's
next turn. A user enables the **capability** (`webreader`), never the `remote`
bridge — the bridge comes up transparently underneath.

## office_extract is a built-in, not a plugin

`internal/tools/office.go`. `office_extract` reads Word / PowerPoint / Excel
(`.docx` / `.pptx` / `.xlsx`) to plain text. Those formats are just a ZIP of XML
parts, so extraction is **pure standard library** (`archive/zip` +
`encoding/xml`) — no third-party dependency. That is exactly why it is a built-in,
registered unconditionally in `main.go` (`"builtin"`) with no build tag, unlike
`pdf`, whose PDF library has to be kept out of the default binary. It also
registers its own `web_fetch` decoders (`RegisterOfficeDecoders`) so linked Office
files read like PDFs do — the same seam pattern, minus the plugin. Legacy binary
`.doc` / `.ppt` / `.xls` are out of scope (OLE containers; "Save As" to the modern
format is the fix).

## Writing a plugin

**Compiled (Go).** A folder under `internal/plugins/<name>/`, one build tag, an
`init()` that calls `plugins.Add`, and a `Register(Host)` that adds tools/seams.
Wire the tag into your build and name it in config `plugins`. `pdf/` is the
reference.

**Remote (any language).** A folder under `plugins/<name>/`, **no Go, no rebuild**:

```
plugins/<name>/
  plugin.py     # MANIFEST dict + invoke(tool, params) -> envelope (sync or async)
  skill.md      # optional: the "when/how" playbook, carried in the manifest
```

Run the host and point kaiju at it:

```sh
pip install -r requirements.txt                  # base — no browser, no Chromium
# optional JS-render tier (heavy: pulls Chromium):
pip install -r requirements-render.txt && playwright install chromium
./start.sh 8092                                  # or: uvicorn host:app --host 127.0.0.1 --port 8092

export KAIJU_PLUGIN_HOST=http://127.0.0.1:8092   # bridge env default is 8091
export KAIJU_PLUGIN_TOKEN=<shared-secret>        # optional; host enforces if set
# build kaiju with -tags plugin_remote and add "remote" to config `plugins`
```

`webreader/` is the reference implementation. Where the host runs and what a
plugin may touch is the operator's policy — kaiju provides the mechanism (a shared
bearer token), not the perimeter.

## Relevant source

| file | responsibility |
|---|---|
| `internal/plugins/registry.go` | `Host` / `Plugin` interfaces, `Add`, `Activate`, `Catalog`, `RemoteCatalog` |
| `internal/plugins/pdf/pdf.go` | `plugin_pdf`: `pdf_extract` tool + `application/pdf` decoder seam |
| `internal/plugins/remote/remote.go` | `plugin_remote`: manifest fetch, synthesized `remoteTool`, reader seam, 60 s ceiling |
| `internal/tools/plugin_manage.go` | `plugin_list` / `plugin_enable` / `plugin_option`, `liveHost`, `ensureRemoteUp`, `EnsureRemoteHostsUp` |
| `internal/tools/service.go` | process supervisor: health loop, auto-restart, `freePort`, port-skip, crash-backoff, `StartManaged` |
| `internal/tools/office.go` | `office_extract` built-in (stdlib, no build tag) + `RegisterOfficeDecoders` |
| `plugins/host.py` | FastAPI gateway: `/health`, `/plugins`, `/invoke/{tool}` |
| `plugins/registry.py` | plugin loading + per-plugin semaphore (per-service queue) |
| `plugins/webreader/plugin.py` | reference remote plugin: static + Playwright render tiers |
| `plugins/start.sh` | venv bootstrap + uvicorn launch (default port 8092) |
| `cmd/kaiju/main.go` | boot wiring: `Activate`, runtime-tool registration, `EnsureRemoteHostsUp` |
