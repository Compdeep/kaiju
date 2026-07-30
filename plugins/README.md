# kaiju plugin host (reference)

An out-of-process, long-running service that hosts **remote plugins** for kaiju.
Kaiju's Go bridge (`internal/plugins/remote`, build tag `plugin_remote`) fetches
this host's manifest at startup and turns every advertised tool into a native
kaiju tool — so adding a plugin here needs **no kaiju rebuild**.

Kaiju has two kinds of plugin:

- **In-process (Go):** compiled into the binary (`internal/plugins/<name>`, build
  tags). For capabilities that need kaiju's internals or the hot path.
- **Remote (this host):** any language, over REST. For the ecosystem — headless
  rendering, ML models, data libraries — and for isolation. This is the reference
  host; the protocol is language-agnostic, so a Node or Rust host would work too.

## The protocol

| Method | Path | Purpose |
|--------|------|---------|
| `GET`  | `/plugins` | The manifest: every plugin, its tools + JSON-schema params + skill. |
| `POST` | `/invoke/{tool}` | Run a tool with `{"params": {...}}`; returns a kaiju ToolMessage envelope. |
| `GET`  | `/health` | Liveness + loaded plugin names. |

A ToolMessage envelope is `{"kind", "status": "ok"|"empty"|"error", "content"?, "detail"?, "data"?}`.

## Writing a plugin

One folder under `plugins/`, no Go, no rebuild:

```
plugins/<name>/
  plugin.py     # MANIFEST dict + invoke(tool, params) -> envelope  (sync or async)
  skill.md      # optional: the "when/how" playbook, carried in the manifest
```

`webreader/` is the reference implementation.

## Run

```sh
pip install -r requirements.txt                 # base — no browser, no Chromium
# OPTIONAL JS-rendering tier (heavy: pulls Chromium, a few hundred MB):
pip install -r requirements-render.txt && playwright install chromium
uvicorn host:app --host 127.0.0.1 --port 8091
```

Without the render tier, `webreader` runs static extraction only and skips
rendering gracefully. On a small box, run the render tier on a separate machine
and point `KAIJU_PLUGIN_HOST` at it.

Point kaiju at it:

```sh
export KAIJU_PLUGIN_HOST=http://127.0.0.1:8091   # default
export KAIJU_PLUGIN_TOKEN=<shared-secret>        # optional; host enforces if set
# build kaiju with -tags plugin_remote and add "remote" to config `plugins`
```

## Notes

- Plugins load **once** at startup and stay **warm** — `webreader` keeps a single
  headless browser alive across every call, not one per request.
- Auth is a shared bearer token (`KAIJU_PLUGIN_TOKEN`). *Where* the host runs and
  what a plugin may touch is the operator's policy — kaiju only provides the
  mechanism. On a small box, run the host (or at least the rendering tier)
  elsewhere and point `KAIJU_PLUGIN_HOST` at it.
