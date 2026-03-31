---
name: github
description: "GitHub operations via gh CLI: issues, PRs, CI runs, code review, API queries. Use when checking PR status, creating issues, listing PRs, or viewing run logs."
metadata:
  requires:
    bins: ["gh"]
---

## When to Use

Use when the user asks about GitHub repositories, issues, pull requests, CI status, or code review. Requires the `gh` CLI authenticated via `gh auth login`.

Do NOT use for local git operations (commit, push, pull) — use the `git` tool directly.

## Planning Guidance

### Check PR status or CI

1. `bash` — `gh pr view <number> --json state,reviews,checks` or `gh pr list`

Single step, no dependencies needed.

### Create an issue

1. `bash` — `gh issue create --title "..." --body "..."`

### List and filter issues or PRs

Plan parallel calls when checking multiple repos or filtering by different criteria:

1. `bash` — `gh issue list --label bug --state open`
2. `bash` — `gh pr list --state open --json number,title,reviews` (parallel with step 0)

### View CI run logs

1. `bash` — `gh run list --limit 5` to find the run
2. `bash` — `gh run view <id> --log-failed` (depends on step 0, use `param_refs` for run ID)

### Code review workflow

1. `bash` — `gh pr diff <number>` to get the diff
2. `file_read` calls in parallel for changed files (depend on step 0)

### Create a PR

1. `bash` — `gh pr create --title "..." --body "..." --base main`

### Repository info

Plan parallel queries:

1. `bash` — `gh repo view --json description,stargazerCount,forkCount`
2. `bash` — `gh release list --limit 5` (parallel with step 0)

### What NOT to do

- Don't use `bash` with raw `curl` for GitHub API — use `gh api` instead
- Don't plan `gh` commands when `gh auth status` hasn't been verified — check auth first if uncertain
- Don't use `git` tool for GitHub-specific operations (issues, PRs, CI) — use `gh`
