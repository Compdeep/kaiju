---
name: gh_issues
description: "Fetch GitHub issues, analyze them, and create PRs with fixes. Use when the user asks to triage issues, implement fixes from issue descriptions, or batch-process bugs."
metadata:
  requires:
    bins: ["gh", "git"]
---

## When to Use

Use when the user asks to:
- List and triage GitHub issues
- Implement a fix for a specific issue
- Batch-analyze open bugs
- Create a PR from an issue description
- Review and respond to PR comments

## Planning Guidance

### Triage open issues

Plan parallel fetches for different labels/filters:

1. `bash` — `gh issue list --state open --label bug --json number,title,body,labels --limit 20`
2. `bash` — `gh issue list --state open --label enhancement --json number,title,body --limit 20` (parallel)

### Implement a fix for a single issue

Read the issue, understand the codebase, fix, test, PR:

1. `bash` — `gh issue view <number> --json title,body,comments`, tag `read_issue`
2. `file_read` for each file the issue names, referencing `read_issue`'s `stdout`. These run at the same time as each other.
3. `file_write` for the fix, referencing what the reads returned
4. `bash` — run the unit tests. It passes no data from the writes, only has to follow them, so `depends_on` orders it.
5. `bash` — `git add . && git commit -m "fix: <description>"`, ordered after the tests
6. `bash` — `gh pr create --title "Fix #<number>: ..." --body "Closes #<number>"`, ordered after the commit

### Batch-analyze issues

1. `bash` — `gh issue list --state open --json number,title,body,labels --limit 50`
2. For each issue, the aggregator synthesizes a triage report — no additional tool calls needed

### Monitor PR review comments

1. `bash` — `gh pr list --state open --json number,title,reviews,reviewRequests`, tag `list_prs`
2. `bash` — `gh pr view <number> --json comments,reviews`, taking the number from `list_prs`'s `stdout`

### Create a branch and PR from issue

1. `bash` — `git checkout -b fix/<issue-number>-<short-desc>`, tag `branch`
2. Implement the fix: read, write, test. Nothing here needs data from `branch`, it only has to happen after it, so `depends_on` orders it.
3. `bash` — `gh pr create --title "..." --body "Closes #<number>"`, ordered after the implementation

### Search across repos

1. `bash` — `gh search issues --repo owner/repo "search query" --json number,title,repository`

### What NOT to do

- Don't create PRs without running tests first
- Don't plan sequential issue fetches when they can be filtered in one query
- Don't modify files referenced in an issue without reading them first
- Don't push to main — always create a branch for fixes
