---
name: kaiju_clawhub
description: "Search, install, update, and manage skills from ClawHub. Use when the user asks to find new skills, install a skill, update installed skills, or check skill info."
---

## When to Use

Use when the user asks to:
- Install a skill from ClawHub
- Search for available skills
- Update installed skills
- Check skill details or versions
- List installed skills

## Planning Guidance

### Install a skill

1. `bash` — `kaiju skill install <slug>`

Single step. The slug can be `owner/name` or just `name`.

### List installed skills

1. `bash` — `kaiju skill list`

Shows all skills with source (clawhub or local) and version.

### Update all skills

1. `bash` — `kaiju skill update`

Checks all ClawHub-installed skills for newer versions and updates them.

### Get skill info

1. `bash` — `kaiju skill info <slug>`

Shows name, version, author, description, download count, and supported platforms.

### Search and install flow

When the user wants to find and install a skill:

1. `web_fetch` — fetch `https://clawhub.ai` with focus on skill listings (or use `web_search` for "clawhub <topic> skill")
2. `bash` — `kaiju skill install <slug>` (depends on step 0, extract slug from results)

### Skill directories

Skills are loaded in precedence order:

1. `<install>/skills/bundled/` — shipped with Kaiju (lowest)
2. `~/.kaiju/skills/` — installed from ClawHub
3. `~/.kaiju/workspace/skills/` — workspace overrides (highest)

A workspace skill with the same name as an installed skill takes precedence.

### What NOT to do

- Don't install skills without confirming with the user first — skills run code on their system
- Don't manually download and extract skills — use `kaiju skill install` which handles versioning and metadata
