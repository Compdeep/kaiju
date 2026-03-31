# Workspace

Kaiju separates **system state** from **user content** using two directory trees.

## Directory Layout

```
~/.kaiju/                           # System state (data_dir)
├── kaiju.db                        # SQLite database (sessions, users, scopes, audit)
├── agent/                          # Agent runtime state (audit logs)
├── skills/                         # User-installed skills from ClawHub
│   ├── playwright/
│   │   ├── SKILL.md
│   │   └── _meta.json
│   └── ...
└── workspace/                      # User workspace (default location)
    ├── AGENTS.md                   # Operating instructions for the agent
    ├── SOUL.md                     # Persona, tone, boundaries
    ├── USER.md                     # Who the user is
    ├── memory/                     # Long-term memory files
    ├── skills/                     # Workspace-specific skill overrides
    │   └── my-custom-skill/
    │       └── SKILL.md
    └── canvas/                     # Panel/canvas files (generated content)
```

## System State vs User Content

| Directory | Contains | Backed up by | Mounted in Docker as |
|-----------|----------|--------------|---------------------|
| `~/.kaiju/` (data_dir) | DB, installed skills, runtime state, JWT keys | System backup | Named volume |
| `~/.kaiju/workspace/` | AGENTS.md, SOUL.md, memory, workspace skills | Git repo (recommended) | Bind mount |

**System state** is managed by Kaiju — you install skills, create users, run queries. It changes through the CLI and API.

**Workspace** is managed by you — edit AGENTS.md to change agent behavior, add skills to override defaults, store project-specific memory. Version it in a private git repo.

## Skill Precedence

Skills are loaded in precedence order. Later sources override earlier ones with the same name.

```
1. <install>/skills/bundled/        Shipped with Kaiju (lowest precedence)
2. ~/.kaiju/skills/                  Installed from ClawHub (kaiju skill install)
3. ~/.kaiju/workspace/skills/        Workspace overrides (highest precedence)
4. skills_dirs (config)              Extra dirs from kaiju.json (appended after defaults)
```

To override a bundled or installed skill, create a workspace skill with the same name:

```
~/.kaiju/workspace/skills/kaiju_coder/SKILL.md
```

This workspace version takes precedence over the bundled `kaiju_coder`.

## Bootstrap Files

On first run, Kaiju seeds the workspace with default bootstrap files:

| File | Purpose |
|------|---------|
| `AGENTS.md` | Operating instructions — tool preferences, boundaries, behavior rules |
| `SOUL.md` | Agent persona — tone, style, identity |
| `USER.md` | User profile — role, expertise, preferences |

These files are read at session start. Edit them to customize the agent.

Bootstrap never overwrites existing files. If you delete one, it will be re-created on next startup.

## Configuration

In `kaiju.json`:

```json
{
  "agent": {
    "data_dir": "~/.kaiju",
    "workspace": "~/.kaiju/workspace"
  }
}
```

- `data_dir` — System state directory. Default: `~/.kaiju`
- `workspace` — User content directory. Default: `<data_dir>/workspace`

Set `workspace` to a custom path to separate it from the system state:

```json
{
  "agent": {
    "data_dir": "~/.kaiju",
    "workspace": "~/projects/my-agent-workspace"
  }
}
```

Environment variables are supported:

```json
{
  "agent": {
    "workspace": "$KAIJU_WORKSPACE"
  }
}
```

## Docker

The workspace separation makes Docker deployment clean:

```yaml
services:
  kaiju:
    image: kaiju:latest
    volumes:
      - kaiju-data:/home/kaiju/.kaiju          # system state (named volume)
      - ./workspace:/home/kaiju/.kaiju/workspace  # user content (bind mount)
    environment:
      - LLM_API_KEY=${LLM_API_KEY}

volumes:
  kaiju-data:
```

Or with a fully external workspace:

```yaml
volumes:
  - kaiju-data:/home/kaiju/.kaiju
  - /path/to/my/workspace:/workspace
environment:
  - KAIJU_WORKSPACE=/workspace
```

This way:
- System state persists in a named volume (survives container rebuilds)
- Workspace is bind-mounted from the host (editable, git-versioned)
- Config and secrets stay in the system volume
- Workspace files are portable between deployments

## Compatibility with OpenClaw

Kaiju's workspace concept is inspired by OpenClaw's agent workspace. Key similarities:

- Bootstrap files (AGENTS.md, SOUL.md, USER.md) serve the same purpose as OpenClaw's workspace bootstrap
- Workspace skills override installed/bundled skills (same precedence model)
- SKILL.md format is compatible — OpenClaw skills work in Kaiju
- ClawHub skills install to the same location (`~/.kaiju/skills/`)

The workspace path is different (`~/.kaiju/workspace/` vs `~/.openclaw/workspace/`) but the structure inside is compatible.
