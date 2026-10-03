# kaiju plugin host (reference)

An out-of-process, long-running service that hosts **Python plugins** for kaiju.
Kaiju's Go bridge (`internal/plugins/remote`, build tag `plugin_remote`) fetches
this host's manifest at startup and turns every advertised tool into a native
kaiju tool.

Kaiju has two kinds of plugin, and the catalogue
(`internal/plugins/catalogue.json`) says which each one is:

- **`kind: "go"`** — compiled into the binary from `internal/plugins/<name>`,
  behind its own tag `plugin_<name>`. For capabilities that need kaiju's internals
  or the hot path.
- **`kind: "python"`** — a folder here, served by this host, over REST. For the
  ecosystem: headless rendering, imaging, ML models, data libraries. Every one of
  them is carried by the one bridge, so they all need the single tag
  `plugin_remote` and none of their own. The protocol is language-agnostic, so a
  Node or Rust host would work in place of this one.

## Installing a plugin

A plugin is either in the binary and live, or it is not installed. There is no
third state and nothing to switch on.

```sh
kaiju serve --plugins illustrator,webreader
```

That is a declaration of what this installation should be. If the binary was not
built with them, kaiju builds one that was, proves it boots (`kaiju selfcheck`),
replaces itself and re-execs — keeping its PID, so a supervisor sees no restart.
Config `plugins` does the same thing and the two union.

Adding a Python plugin here still needs `plugin_remote` in the binary. It is one
tag for all of them, so the second and later ones need no rebuild at all.

The agent can do this itself with `plugin_install`, when the operator has set
`allow_runtime_plugin_activation`. It needs kaiju's source tree and a Go toolchain
on the machine; without them it reports that it cannot, which is the honest answer.

## The protocol

| Method | Path | Purpose |
|--------|------|---------|
| `GET`  | `/plugins` | The manifest: every plugin, its tools + JSON-schema params + skill. |
| `POST` | `/invoke/{tool}` | Run a tool with `{"params": {...}}`; returns a kaiju ToolMessage envelope. |
| `GET`  | `/health` | Liveness + loaded plugin names. |

A ToolMessage envelope is
`{"type", "status": "ok"|"empty"|"error", "content"?, "detail"?, "data"?}`.

The field is **`type`**. This file and `registry.py`'s `envelope()` helper both
said `kind` long after the Go side renamed it, so an envelope built through the
helper was reshaped by the bridge's fallback instead of passing through as the
ToolMessage it already was.

## Writing a plugin

One folder, no Go:

```
plugins/<name>/
  plugin.py          # MANIFEST dict + invoke(tool, params) -> envelope  (sync or async)
  skill.md           # optional: when and how to use the tools — pure guidance
  install.md         # optional: what an installer has to do beyond pip
  requirements.txt   # the base tier; the launcher installs it
```

`skill.md` needs **no YAML frontmatter**. The bridge adds it from the manifest's
name and description, because a card repeating those would be stating the same two
facts twice and free to disagree with itself. Write the guidance and nothing else.

Then add the plugin to `internal/plugins/catalogue.json`, which is what
`plugin_list` reads and what `--plugins` resolves against. A folder that is not in
the catalogue cannot be installed by name.

`webreader/` and `illustrator/` are the reference implementations.

## Run

The launcher does the setup and is what kaiju's service manager starts:

```sh
./start.sh 8092 /path/to/workspace     # Linux and macOS
./start.ps1 -Port 8092                 # Windows, untested
```

It creates its virtualenv under `${XDG_DATA_HOME:-~/.local/share}/kaiju/plugin-host`
— **not** beside this README, where it put 269 MB of runtime into the source tree —
installs this host's requirements and then each plugin's own base tier, and execs
uvicorn. `KAIJU_PLUGIN_VENV` overrides the location.

A heavier tier is opt-in. `webreader`'s rendering tier pulls Chromium:

```sh
pip install -r requirements-render.txt && playwright install chromium
```

Without it, `webreader` does static extraction only and skips rendering rather than
failing. On a small box, run the host elsewhere and point `KAIJU_PLUGIN_HOST` at it.

## Environment

| Variable | Meaning |
|---|---|
| `KAIJU_PLUGINS` | Which plugins to load, comma separated. Unset, every folder with a `plugin.py` is loaded — which is how the host and kaiju's config came to disagree about what was enabled. |
| `KAIJU_WORKSPACE` | The sandbox root. The protocol carries no workspace, so a plugin that touches files enforces this itself; unset, such a plugin refuses every path. |
| `KAIJU_PLUGIN_HOST` | Where kaiju's bridge looks for this host. Default `http://127.0.0.1:8091`; the catalogue's Python plugins declare 8092. |
| `KAIJU_PLUGIN_TOKEN` | Shared bearer token. The host enforces it when set. |
| `KAIJU_PLUGIN_VENV` | Where the launcher builds its virtualenv. |

## Notes

- Plugins load **once** at startup and stay **warm** — `webreader` keeps a single
  headless browser alive across every call, not one per request.
- A plugin may not take a builtin's tool name. The registry replaces by name, so a
  host advertising `bash` would have silently replaced the real one; kaiju now
  refuses the tool and logs it.
- Compiling a plugin links its code into kaiju's own binary: no sandbox, no path
  check, no impact tier of its own. A **core** plugin is in this repository and
  arrived by pull request. A **community** plugin carries a pinned source and
  commit in the catalogue, and adding one is an operator action — the agent never
  fetches and compiles code it chose.
- *Where* the host runs and what a plugin may touch is the operator's policy. Kaiju
  provides the mechanism.
