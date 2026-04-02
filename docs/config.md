# Configuration Reference

Kaiju loads config from (in priority order):
1. `--config <path>` flag
2. `KAIJU_CONFIG` environment variable
3. `./kaiju.json` (current directory)
4. `~/.kaiju/config.json`
5. Built-in defaults

Environment variables can be referenced as `${VAR_NAME}` in any string field.

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

  "agent": {
    "dag_enabled": true,
    "dag_mode": "orchestrator",
    "max_nodes": 30,
    "max_per_skill": 5,
    "max_llm_calls": 20,
    "max_observer_calls": 50,
    "batch_size": 5,
    "wall_clock_sec": 120,
    "max_turns": 15,
    "rate_limit": 100,
    "safety_level": 1,
    "data_dir": "~/.kaiju",
    "workspace": "~/.kaiju/workspace",
    "planner_mode": "native",
    "embeddings": {
      "enabled": false,
      "endpoint": "",
      "api_key": "",
      "model": "text-embedding-3-small",
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
    "enabled": true,
    "port": 8081,
    "auth_token": "${KAIJU_API_TOKEN}"
  },

  "tools": {
    "bash": { "enabled": true, "shell": "auto" },
    "file": { "enabled": true, "allowed_paths": ["."] },
    "web": {
      "enabled": true,
      "search_provider": "startpage+ddg",
      "search_delay_sec": 0.2
    },
    "sysinfo": { "enabled": true }
  },

  "skills_dirs": ["~/.kaiju/skills"]
}
```

## Section Reference

### `llm`

| Field | Default | Description |
|-------|---------|-------------|
| `provider` | `"openai"` | LLM provider: `openai`, `anthropic` |
| `endpoint` | `"https://api.openai.com/v1"` | API base URL |
| `api_key` | — | API key (required). Env: `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, or `LLM_API_KEY` |
| `model` | `"gpt-4o"` | Model identifier |
| `temperature` | `0.3` | Sampling temperature |
| `max_tokens` | `4096` | Max output tokens per LLM call |

### `agent`

| Field | Default | Description |
|-------|---------|-------------|
| `dag_enabled` | `true` | Enable DAG parallel execution (false = ReAct fallback for async triggers) |
| `dag_mode` | `"reflect"` | Default DAG mode: `reflect`, `nReflect`, `orchestrator` |
| `max_nodes` | `30` | Max total DAG nodes per investigation |
| `max_per_skill` | `5` | Max invocations of a single skill per wave. Resets at reflection boundaries. |
| `max_llm_calls` | `20` | Max LLM calls per investigation (planner + reflections + aggregator) |
| `max_observer_calls` | `50` | Max observer LLM calls (orchestrator mode) |
| `batch_size` | `5` | Skill completions before reflection (nReflect mode) |
| `wall_clock_sec` | `120` | Investigation timeout in seconds |
| `max_turns` | `15` | Max ReAct loop turns |
| `rate_limit` | `1000` | Max tool invocations per hour |
| `safety_level` | `1` | Default IGX intent: 0=observe, 1=operate, 2=override |
| `data_dir` | `"~/.kaiju"` | Data directory for memory, audit logs, skills |
| `workspace` | `"~/.kaiju/workspace"` | Working directory for bash tool execution (downloads, file creation) |
| `planner_mode` | `"native"` | Planner mode: `native` (function calling) or `structured` (JSON text) |

### `agent.embeddings`

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `false` | Enable semantic skill routing via embeddings |
| `model` | `"text-embedding-3-small"` | Embedding model |
| `top_k` | `8` | Max skills to present to planner |
| `threshold` | `0.3` | Minimum similarity score |

### `channels`

| Channel | Fields | Description |
|---------|--------|-------------|
| `cli` | `enabled` | Interactive stdin/stdout chat |
| `web` | `enabled`, `port` | WebSocket channel on gateway |
| `telegram` | `enabled`, `token` | Telegram Bot API (v0.2) |
| `discord` | `enabled`, `token` | Discord bot (v0.2) |

### `api`

| Field | Default | Description |
|-------|---------|-------------|
| `enabled` | `true` | Enable REST API |
| `port` | `8081` | API port |
| `auth_token` | — | Bearer token for API auth (empty = no auth) |

### `tools`

| Tool | Fields | Description |
|------|--------|-------------|
| `bash` | `enabled`, `shell` | Shell execution. `shell`: `auto`, `sh`, `powershell`, `cmd`. Working directory defaults to `workspace`. |
| `file` | `enabled`, `allowed_paths` | File read/write/list |
| `web` | `enabled`, `search_provider`, `search_delay_sec` | Web search and fetch. `search_provider`: `startpage`, `ddg`, or `startpage+ddg` (default). `search_delay_sec`: minimum seconds between search requests (default `0.2`). |
| `sysinfo` | `enabled` | System information |

### `skills_dirs`

Array of directories to scan for SKILL.md user-defined skills. Hot-reloaded on file changes.

## DAG Modes

| Mode | Behavior | Best for |
|------|----------|----------|
| `reflect` | Serialized with reflection barriers between depth waves | Conservative, high-stakes tasks |
| `nReflect` | Parallel with batched reflection every N completions | Balanced autonomy/oversight |
| `orchestrator` | Parallel with per-node observer LLM calls | Interactive chat, maximum responsiveness |

## Safety Levels

| Level | Name | Allowed tools |
|-------|------|---------------|
| 0 | tell | Read-only: sysinfo, file_read, web_fetch |
| 1 | triage | + side effects: file_write, bash (non-destructive) |
| 2 | act | + destructive: bash (rm, kill), system changes |

Default is `1` (triage). Can be overridden per-request via the API `intent` field.

## Environment Variables

| Variable | Used for |
|----------|----------|
| `OPENAI_API_KEY` | OpenAI API key (auto-detected) |
| `ANTHROPIC_API_KEY` | Anthropic API key (auto-detected, sets provider/model) |
| `LLM_API_KEY` | Generic LLM API key |
| `KAIJU_CONFIG` | Config file path |
| `KAIJU_API_TOKEN` | API auth token |
| `TELEGRAM_BOT_TOKEN` | Telegram bot token |
| `DISCORD_BOT_TOKEN` | Discord bot token |

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
