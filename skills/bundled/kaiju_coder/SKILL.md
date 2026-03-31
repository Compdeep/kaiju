---
name: kaiju_coder
description: Coding workflows — write, refactor, debug, and review code using file and bash tools with parallel planning guidance
metadata:
  requires:
    bins: ["git"]
---

## When to Use

Use when the user asks to write, modify, debug, review, or refactor code. Also applies to: generating unit tests, explaining code in context, fixing build errors, or creating new files in a project.

Do NOT use for questions answerable from knowledge alone — those should return an empty plan via general_assistant.

## Planning Guidance

### Plan the full picture before writing anything

Before planning any writes, map out the affected code: function signatures, return types, shared interfaces, callers, and callees. If two files interact — one defines a function and another calls it — plan both changes together with consistent signatures, arguments, and return values. Never plan a write to a function's signature without also planning writes to every file that depends on it.

Parallel writes are only safe when the files are independent. If files share types, call each other, or feed into the same build target, plan them in sequence or ensure every write in the batch accounts for the others.

### Read before write

Always plan `file_read` steps before any `file_write` steps for the same file. Never overwrite a file you haven't read. Link writes to their reads via `depends_on`.

### Single-file change

Plan three steps:

1. `file_read` — read the target file
2. `file_write` — apply the change (depends on the read)
3. `bash` — run unit tests or linter to verify (depends on the write)

### Multi-file refactor

Plan parallel reads, then parallel writes, then verify:

1. `file_read` calls in parallel for every affected file (steps 0 through N)
2. `file_write` calls in parallel, each depending on its own read step
3. `bash` — run the full unit test suite once all writes complete

Maximize parallelism: files that don't depend on each other should be read and written in parallel batches.

### Debugging a failing test or build

1. `bash` — reproduce the failure (run the exact failing command)
2. `file_read` — read the file(s) referenced in the error output, use `param_refs` to chain from the bash output when possible
3. `file_write` — apply the fix (depends on the read)
4. `bash` — re-run the original command to confirm (depends on the write)

### Adding unit tests

1. `file_read` — read the source file being tested
2. `file_read` — read existing test file if one exists (parallel with step 0)
3. `file_write` — write or append test cases (depends on both reads)
4. `bash` — run the new tests (depends on the write)

### Code review

Read-only — no writes needed. Plan parallel reads then synthesize.

1. `bash` — `git diff` or `git log` to identify changed files
2. `file_read` calls in parallel for each changed file (depend on step 0, use `param_refs` to extract paths)

### Project discovery

When the user references a project you haven't seen:

1. `file_list` — list the project root
2. `file_read` calls in parallel for key files: README, package.json / go.mod / Cargo.toml / pyproject.toml, and main entry point
3. `bash` — `git log --oneline -10` for recent context (parallel with reads)

### What NOT to do

- Don't plan parallel writes to files that share code paths — if function A calls function B, plan both changes together so signatures, arguments, and return types stay consistent
- Don't plan `file_write` without a preceding `file_read` of the same file
- Don't plan files sequentially when they are genuinely independent — parallelize reads and writes for unrelated files
- Don't skip the verification step — always run unit tests or build after writes
- Don't use `web_search` or `web_fetch` for coding tasks unless the user explicitly asks to look something up
- Don't plan a single `bash` call to do everything — prefer structured file operations so each step is observable and recoverable
- Don't modify a function's interface without reading and updating all its callers in the same plan
