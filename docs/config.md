# Configuration Reference

Kaiju loads config from (in priority order):
1. `--config <path>` flag
2. `KAIJU_CONFIG` environment variable
3. `./kaiju.json` (current directory)
4. `~/.kaiju/config.json`
5. Built-in defaults

Environment variables can be referenced as `${VAR_NAME}` in any string field.

Every default listed below is the actual value returned by `config.Default()`
(`internal/config/defaults.go`). Fields that `Default()` leaves empty resolve to
their Go zero value unless a lane fills them in at runtime — those cases are
called out.

## Full Config Structure

```json
{
  "llm": {
    "provider": "openai",
    "endpoint": "https://api.openai.com/v1",
    "api_key": "${OPENAI_API_KEY}",
    "model": "gpt-4o",
    "temperature": 0.3,
    "max_tokens": 4096
  },

  "executor": {
    "provider": "",
    "endpoint": "",
    "api_key": "",
    "model": ""
  },

  "chat": {
    "provider": "openai",
    "model": "gpt-4o-mini",
    "tools": ["web_fetch"]
  },

  "vision": {
    "provider": "",
    "model": ""
  },

  "providers": {
    "openai":     { "type": "openai",    "endpoint": "https://api.openai.com/v1",  "api_key": "${OPENAI_API_KEY}" },
    "anthropic":  { "type": "anthropic", "endpoint": "https://api.anthropic.com/v1", "api_key": "${ANTHROPIC_API_KEY}" },
    "openrouter": { "type": "openai",    "endpoint": "https://openrouter.ai/api/v1", "api_key": "${OPENROUTER_API_KEY}" },
    "selfhosted": { "type": "openai",    "endpoint": "http://localhost:8000/v1",   "api_key": "" }
  },

  "agent": {
    "dag_enabled": true,
    "dag_mode": "orchestrator",
    "max_nodes": 100,
    "max_per_skill": 10,
    "max_llm_calls": 20,
    "max_observer_calls": 50,
    "batch_size": 5,
    "max_investigations": 5,
    "max_replans": 3,
    "max_concurrent": 3,
    "disable_coding": false,
    "execution_mode": "interactive",
    "route_provider": "openrouter",
    "route_model": "openai/gpt-4.1-mini",
    "wall_clock_sec": 180,
    "max_turns": 15,
    "rate_limit": 100,
    "safety_level": 100,
    "data_dir": "~/.kaiju",
    "workspace": "~/.kaiju/workspace",
    "classifier_enabled": true,
    "intents": [
      { "name": "observe",  "rank": 0,   "builtin": true,                  "description": "Read-only — inspect data and state without making changes", "prompt_description": "Read-only actions that inspect data or state. Look up, analyze, check status, list files, read configs. No side effects, nothing created or modified." },
      { "name": "operate",  "rank": 100, "builtin": true, "default": true, "description": "Normal work — reversible side effects",                   "prompt_description": "Actions with reversible side effects. Write files, modify state, create resources, install dependencies, run code, start services, configure settings. The default working level for real tasks." },
      { "name": "override", "rank": 200, "builtin": true,                  "description": "Destructive — irreversible actions",                        "prompt_description": "Destructive or irreversible actions. Delete, remove, drop, kill, purge, force, wipe, uninstall. Requires explicit elevation." }
    ],
    "embeddings": {
      "enabled": false,
      "endpoint": "",
      "api_key": "",
      "model": "",
      "top_k": 8,
      "threshold": 0.3
    }
  },

  "channels": {
    "cli": { "enabled": true },
    "web": { "enabled": true, "port": 8080 },
    "telegram": { "enabled": false, "token": "${TELEGRAM_BOT_TOKEN}" },
    "discord": { "enabled": false, "token": "${DISCORD_BOT_TOKEN}" }
  },

  "api": {
    "enabled": false,
    "port": 8081,
    "auth_token": "${KAIJU_API_TOKEN}",
    "jwt_secret": ""
  },

  "tools": {
    "bash": { "enabled": true, "shell": "auto" },
    "file": { "enabled": true, "allowed_paths": ["."] },
    "web": {
      "enabled": true,
      "search_provider": "startpage+ddg",
      "search_delay_sec": 0.2
    },
    "sysinfo": { "enabled": true },
    "compute": { "enabled": true, "timeout_sec": 120 }
  },

  "skills_dirs": ["~/.kaiju/skills"],
  "plugins": []
}
```

## Model Lanes

Kaiju runs several model "lanes", each configurable independently. `llm` is the
only required one; the rest fall back to it (or to the executor) when empty.

| Lane | Block | Used for |
|------|-------|----------|
| Reasoning | `llm` | Planner, aggregator, classifier/preflight — the primary brain |
| Executor | `executor` | Reflection, observer, micro-planner, compactor. Empty ⇒ reuses `llm` |
| Chat | `chat` | Direct-completion conversation lane (no planner/DAG). Empty ⇒ reasoning model |
| Vision | `vision` | Answers questions about attached images via direct completion. Empty ⇒ no dedicated lane (images fall back to the reasoning model if it supports vision) |
| Router | `agent.route_provider` / `agent.route_model` | The cheap per-turn "chat vs. investigate" routing decision |

The `providers` block (below) is a separate concern: it is the **credential
catalog** that per-request model routing draws from. A lane's own `provider`
field names an entry in that catalog.

### `llm`

Configures the reasoning model. Endpoint and key can be set inline here (the
legacy single-provider path) or drawn from the `providers` catalog by name.

| Field | Default | Description |
|-------|---------|-------------|
| `provider` | `"openai"` | LLM provider: `openai`, `anthropic` |
| `endpoint` | `"https://api.openai.com/v1"` | API base URL |
| `api_key` | — | API key (required). Env: `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, or `LLM_API_KEY` |
| `model` | `"gpt-4o"` | Model identifier |
| `temperature` | `0.3` | Sampling temperature |
| `max_tokens` | `4096` | Max output tokens per LLM call |

### `executor`

Optional secondary model for the cheaper background roles (reflection, observer,
micro-planner, compactor). Every field is optional; an empty block means the
reasoning model does this work too.

| Field | Default | Description |
|-------|---------|-------------|
| `provider` | — | Provider name; empty ⇒ same as `llm` |
| `endpoint` | — | API base URL; empty ⇒ same as `llm` |
| `api_key` | — | Empty ⇒ falls back to `llm.api_key` |
| `model` | — | Empty ⇒ same as `llm.model` |

### `chat`

The chat lane is a direct completion with **no planner, DAG, or tools** — for
plain conversation and models that can't tool-call (roleplay fine-tunes, etc.).

| Field | Default | Description |
|-------|---------|-------------|
| `provider` | `"openai"` | Provider name; empty ⇒ reasoning model |
| `model` | `"gpt-4o-mini"` | Chat model. A tool-capable, cheap model is preferred; empty ⇒ reasoning model |
| `tools` | `["web_fetch"]` | Default chat-lane tool allowlist, used when a request sends no `chat_tools` of its own. Empty ⇒ pure chat. Include `"agent"` to let chat delegate deep, multi-step work to the full executive |

### `vision`

The vision lane answers questions about attached images with a direct
completion, so a tool-less vision model works and image attachments always route
to a capable model. Both fields optional; empty ⇒ no dedicated lane.

| Field | Default | Description |
|-------|---------|-------------|
| `provider` | — | Provider name from the `providers` catalog |
| `model` | — | Vision-capable model identifier |

## `providers`

The credential catalog for per-request model routing. Each entry is a **named
provider** holding just an endpoint and a key; the host (e.g. makeen) selects a
`provider` + `model` per request and kaiju resolves the name to the keyed client
here. Keys live **only** here — callers supply a selection, never a key.

`Default()` ships **no** providers block; add the ones you need. This block
coexists with the legacy `llm`/`executor` blocks: those carry their own inline
endpoint+key for the default lanes, while `providers` backs anything selected by
name per request. When both are present, a lane that names a `provider` resolves
its credentials from the catalog.

Each entry (`ProviderConfig`):

| Field | Default | Description |
|-------|---------|-------------|
| `type` | map key, else `"openai"` | Wire protocol: `openai` (default) or `anthropic`. A self-hosted OpenAI-compatible endpoint should set `"openai"` and point `endpoint` at the private host |
| `endpoint` | — | Provider API base URL |
| `api_key` | — | Provider API key (supports `${VAR}`) |

Conventional keys are `openai`, `anthropic`, `openrouter`, and `selfhosted`, but
any name works as long as the host selects it.

### `agent`

| Field | Default | Description |
|-------|---------|-------------|
| `dag_enabled` | `true` | Enable DAG parallel execution (false = ReAct fallback for async triggers) |
| `dag_mode` | `"orchestrator"` | Default DAG mode: `reflect`, `nReflect`, `orchestrator` |
| `max_nodes` | `100` | Max total DAG nodes per investigation |
| `max_per_skill` | `10` | Max invocations of a single skill per wave. Resets at reflection boundaries |
| `max_llm_calls` | `20` | Max LLM calls per investigation (planner + reflections + aggregator) |
| `max_observer_calls` | `50` | Max observer LLM calls (orchestrator mode) |
| `batch_size` | `5` | Skill completions before reflection (nReflect mode) |
| `max_investigations` | `5` | Max chained investigations per request (replan loop ceiling) |
| `max_replans` | `3` | Max replans within a single investigation |
| `max_concurrent` | `3` | Scheduler worker-pool size (concurrent investigations). `0` ⇒ default `3` |
| `disable_coding` | `false` | `true` = refuse deep compute (codebase building). Enterprise deployments set this |
| `execution_mode` | `"interactive"` | `interactive` (default) or `autonomous` |
| `route_provider` | `"openrouter"` | Provider for the per-turn chat-vs-investigate routing decision. Empty ⇒ executor lane. See below |
| `route_model` | `"openai/gpt-4.1-mini"` | Model for the routing decision. See below |
| `wall_clock_sec` | `180` | Investigation timeout in seconds |
| `max_turns` | `15` | Max ReAct loop turns |
| `rate_limit` | `100` | Max tool invocations per hour |
| `safety_level` | `100` | Default IGX intent rank. Builtin ranks: `0`=observe, `100`=operate, `200`=override. Custom ranks defined via the intent registry are also accepted |
| `data_dir` | `"~/.kaiju"` | Data directory for memory, audit logs, skills |
| `workspace` | `"~/.kaiju/workspace"` | Working directory for bash tool execution. Empty ⇒ `<data_dir>/workspace` |
| `classifier_enabled` | `true` | Enable the pre-plan preflight LLM call (selects skill guidance, infers intent, routes chat/meta queries, hints required tool categories). Disabling degrades behavior and is only useful for tests |

#### `agent.route_provider` / `agent.route_model`

These pin the model for the cheap, per-turn **routing decision** (preflight):
"does this query need the full agent, or just a chat reply?" The router runs on a
tight ~16-token budget, so it wants a small, **non-reasoning** tool-caller.

The default `openrouter` / `openai/gpt-4.1-mini` benched 100% route-accuracy,
100% budget-fit, and ~700 ms; a reasoning model (e.g. `gpt-5-mini`) starves at 16
tokens and silently falls back to chat. See `docs/router-model-bench.md` for the
full comparison. Empty ⇒ the executor lane. Overridable via config, the config
API, or the CLI.

#### `agent.intents`

Seeds the intent registry on **first run only**. Once the DB has any `intents`
rows, this list is ignored — the DB is authoritative and admins edit intents via
the UI. Go code only ever sees ranks; names are purely presentation/config data,
so this list can be replaced wholesale.

| Field | Description |
|-------|-------------|
| `name` | Intent name (referenced by the API `intent` field) |
| `rank` | Impact rank the gate enforces against tool impact |
| `description` | Short human-facing label |
| `prompt_description` | Longer text the classifier reads to infer intent from a query |
| `builtin` | `true` for the shipped observe/operate/override trio |
| `default` | Exactly one intent should be marked `default` (the operate seed is) |

Seed ladder:

| Rank | Name | Default | Meaning |
|------|------|---------|---------|
| 0 | observe | | Read-only — inspect data and state without making changes |
| 100 | operate | ✓ | Normal work — reversible side effects |
| 200 | override | | Destructive — irreversible actions |

### `agent.embeddings`

Semantic skill routing. `Default()` does **not** populate this block, so every
field is off/empty unless you set it. When enabled but a field is left empty,
runtime fallbacks apply as noted.

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `false` | Enable semantic skill routing via embeddings |
| `endpoint` | — | Embedding API base URL. Empty ⇒ falls back to `llm.endpoint` |
| `api_key` | — | Empty ⇒ falls back to `llm.api_key` |
| `model` | — | Embedding model. No built-in default — set this (e.g. `text-embedding-3-small`), or routing stays disabled |
| `top_k` | `8` | Max skills to present to planner. `≤ 0` ⇒ `8` at runtime |
| `threshold` | `0.3` | Minimum similarity score. `≤ 0` ⇒ `0.3` at runtime |

### `channels`

| Channel | Fields | Default | Description |
|---------|--------|---------|-------------|
| `cli` | `enabled` | `true` | Interactive stdin/stdout chat |
| `web` | `enabled`, `port` | `true`, `8080` | WebSocket channel on gateway |
| `telegram` | `enabled`, `token` | `false`, — | Telegram Bot API (v0.2) |
| `discord` | `enabled`, `token` | `false`, — | Discord bot (v0.2) |

### `api`

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `false` | Enable REST API |
| `port` | `8081` | API port |
| `auth_token` | — | Legacy bearer token for API auth (empty = no bearer auth). Env: `KAIJU_API_TOKEN` |
| `jwt_secret` | — | JWT signing secret. Auto-generated if empty |

### `tools`

| Tool | Fields | Default | Description |
|------|--------|---------|-------------|
| `bash` | `enabled`, `shell` | `true`, `"auto"` | Shell execution. `shell`: `auto`, `sh`, `powershell`, `cmd` (`auto` resolves to `sh`, or `powershell` on Windows). Working directory defaults to `workspace` |
| `file` | `enabled`, `allowed_paths` | `true`, `["."]` | File read/write/list |
| `web` | `enabled`, `search_provider`, `search_delay_sec` | `true`, `"startpage+ddg"`, `0.2` | Web search and fetch. `search_provider`: `startpage`, `ddg`, or `startpage+ddg`. `search_delay_sec`: minimum seconds between search requests. Both fall back to these values at runtime when unset |
| `sysinfo` | `enabled` | `true` | System information |
| `compute` | `enabled`, `timeout_sec` | `true`, `120` | LLM-powered compute nodes. `timeout_sec`: max code execution time |

### `skills_dirs`

Array of directories to scan for SKILL.md user-defined skills. Hot-reloaded on
file changes. Default `["~/.kaiju/skills"]`.

### `plugins`

Array naming the optional, build-tag-gated plugins to switch on at startup
(e.g. `["pdf"]`). A name here only takes effect if the binary was compiled with
that plugin's tag (`-tags plugin_pdf`); otherwise it's reported as missing and
ignored. Default `[]` (none). See `internal/plugins`.

## DAG Modes

| Mode | Behavior | Best for |
|------|----------|----------|
| `reflect` | Serialized with reflection barriers between depth waves | Conservative, high-stakes tasks |
| `nReflect` | Parallel with batched reflection every N completions | Balanced autonomy/oversight |
| `orchestrator` | Parallel with per-node observer LLM calls | Interactive chat, maximum responsiveness (default) |

## Intent Levels

The gate enforces `tool.impact <= min(intent, clearance, scope_cap)`. Builtin
intents ship at ranks `0` (observe), `100` (operate), and `200` (override).
Admins can add custom intents at any rank via the registry (admin UI → Intents
tab, or the `agent.intents` seed list in the config file) — these flow through the
gate and are selectable anywhere the builtins are.

| Rank | Builtin name | Typical allowed tools |
|------|--------------|------------------------|
| 0    | observe      | Read-only: sysinfo, file_read, web_fetch |
| 100  | operate      | + side effects: file_write, bash (non-destructive) |
| 200  | override     | + destructive: bash (rm, kill), system changes |

Default is `100` (operate). Can be overridden per-request via the API `intent`
field, which accepts any name registered in the intent registry — builtin or
custom. When no `intent` is provided, the planner auto-infers the lowest
sufficient rank from the tools it selects.

## Environment Variables

| Variable | Used for |
|----------|----------|
| `OPENAI_API_KEY` | OpenAI API key (auto-detected) |
| `ANTHROPIC_API_KEY` | Anthropic API key (auto-detected, sets provider/model) |
| `OPENROUTER_API_KEY` | OpenRouter API key (referenced by the `providers` catalog / router lane) |
| `LLM_API_KEY` | Generic LLM API key |
| `KAIJU_CONFIG` | Config file path |
| `KAIJU_API_TOKEN` | API auth token |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token |
| `DISCORD_BOT_TOKEN` | Discord bot token |

Any string field accepts `${VAR_NAME}`, so any environment variable can be
referenced — not just the ones above.

## Minimal Config

For quick start with just an API key:

```json
{
  "llm": {
    "api_key": "sk-..."
  }
}
```

Everything else uses defaults. Or skip the config file entirely:

```bash
export OPENAI_API_KEY=sk-...
./kaiju chat
```
