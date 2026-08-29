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

1. `file_list` — check whether the skill directory already exists, tag `check_dir`
2. `file_write` — write the SKILL.md file, referencing `check_dir` so it runs after the check

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

Only these six headings are ever extracted from a card, each by the stage named beside it. Everything else in the body is read by people and never reaches a model.

- `## Planning Guidance` — goes to the planner. How to break this kind of task into steps.
- `## RULES` — also goes to the planner, alongside Planning Guidance. Use it for the constraints that must hold, not for method.
- `## Architect Guidance` — goes to the architect inside a `compute` node, which lays out a multi-file build.
- `## Coder Guidance` — goes to the coder inside a `compute` node, which writes each file.
- `## Debug Guidance` — goes to the stage that diagnoses a failure at runtime.
- `## Aggregator Guidance` — goes to the stage that writes the final answer. Use it to say what shape the answer should take.

A card needs `## Planning Guidance` or `## RULES`. With neither, the whole body is dropped and the card reaches the planner as its name and description alone. Kaiju logs a warning at startup when it loads such a card.

Sections like `## When to Use` and `## What NOT to do` are conventional and useful to a reader, but no stage extracts them. Anything the planner must act on belongs under `## Planning Guidance` or `## RULES`.

### Planning Guidance format

Planning Guidance teaches the planner how to structure tool calls. Each pattern should:

1. Name the tools to use (`bash`, `file_read`, `file_write`, `web_search`, and so on). Use names that are in the registry; a card naming a tool that does not exist sends the planner after nothing.
2. Show which steps can run at the same time and which must wait.
3. Say in plain language what an earlier step gives a later one.

Every step carries a `tag`, which is its name in the plan. A later step reads an earlier one by referencing that tag, and the reference is what makes it wait — no separate dependency is declared. `depends_on` exists for the one case where two steps pass no data and still need an order, such as an install before the command that uses it.

Describe the data flow in plain language: "the URL from the search", "the file the error names". A card may name a tag and a field so the planner has something concrete to wire, but it must not write a reference that would resolve. `${step.N.field}` names a particular step, and a card cannot know which steps a plan will contain — the planner decides that and does the wiring. Naming the shape, as this paragraph does, is fine; writing a live one is a guess that breaks the moment a plan is ordered differently.

Example pattern:
```
### Do something

1. `tool_a` — what it does, tag `first`
2. `tool_b` — reads `first`'s output field, so it runs after it
3. `tool_c` — needs nothing from either, so it runs at the same time as `tool_b`
```

### Improve an existing skill

1. `file_read` — read the current SKILL.md, tag `read_card`
2. `file_write` — write the improved version, referencing `read_card`'s content

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
- Don't put guidance the planner needs under a heading no stage extracts — see the six above
