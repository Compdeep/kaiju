---
name: skill_creator
description: "Create, edit, and validate SKILL.md files. Use when the user asks to create a new skill, improve an existing skill, or audit a skill directory."
---

## When to Use

Use when the user asks to:
- Create a new skill from scratch
- Improve, review, or audit an existing SKILL.md
- Restructure a skill directory
- Validate a skill against the SKILL.md format

## Planning Guidance

### Create a new skill

1. `file_list` — check if the skill directory already exists
2. `file_write` — write the SKILL.md file (depends on step 0)

The SKILL.md must follow this structure:

```yaml
---
name: skill_name
description: "One-line description — when to use this skill"
metadata:
  requires:
    bins: ["required_binary"]     # optional: binaries that must be in PATH
  os: ["linux", "darwin"]          # optional: platform restriction
---
```

Required sections in the body:

- `## When to Use` — when the planner should activate this skill
- `## Planning Guidance` — how to decompose tasks into parallel/sequential steps

Optional sections:

- `## Approach Selection` — decision matrix for choosing between approaches
- `## What NOT to do` — anti-patterns and guardrails

### Planning Guidance format

Planning Guidance teaches the DAG planner how to structure tool calls. Each pattern should:

1. Name the tools to use (`bash`, `file_read`, `file_write`, `web_search`, etc.)
2. Show which steps run in parallel vs sequential
3. Indicate dependencies between steps
4. Mention `param_refs` when output from one step feeds into another

Example pattern:
```
### Do something

1. `tool_a` — description of what it does
2. `tool_b` — depends on step 0, use param_refs for chaining
3. `tool_c` — parallel with step 1
```

### Improve an existing skill

1. `file_read` — read the current SKILL.md
2. `file_write` — write the improved version (depends on step 0)

### Validate a skill

1. `file_read` — read the SKILL.md
2. Check: has frontmatter with `name` and `description`
3. Check: has `## When to Use` or `## Planning Guidance` section
4. Check: planning patterns reference real tool names

### Install location

- Bundled skills: `<install>/skills/bundled/<name>/SKILL.md`
- User-installed: `~/.kaiju/skills/<name>/SKILL.md`
- Workspace skills: `~/.kaiju/workspace/skills/<name>/SKILL.md`

Workspace skills override installed and bundled skills with the same name.

### What NOT to do

- Don't create skills that duplicate built-in tool functionality — skills teach the planner HOW to use tools, they don't replace tools
- Don't write overly broad descriptions — the planner uses the description to decide when to activate the skill
- Don't skip the Planning Guidance section — without it, the planner won't know how to decompose tasks for this skill
- Don't reference tools that don't exist in the registry
