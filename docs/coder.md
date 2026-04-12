# Compute Code Pipeline

## Overview

The compute pipeline handles all code generation and editing. It has two modes:
- **Deep** (architect) — plans a multi-file project, writes a blueprint, decomposes into tasks
- **Shallow** (coder) — writes or edits a single file

## Flow Diagram

```
                 ┌──────────────────────────┐
                 │  Executive plans          │
                 │  compute(deep) or         │
                 │  compute(shallow)         │
                 └────────────┬─────────────┘
                              │
                    ┌─────────┴─────────┐
                    │                   │
                    ▼                   ▼
            mode = "deep"        mode = "shallow"
            blueprint_ref=""     (or deep + blueprint_ref)
                    │                   │
                    ▼                   ▼
             ARCHITECT              CODER
                    │                   │
    ┌───────────────┤                   │
    │               │                   │
    ▼               ▼                   │
 scanWorkspace   ScanFunctionMap        │
 Deep (files +   (regex, 20 langs,     │
  small content)  start+end lines)     │
    │               │                   │
    └───────┬───────┘                   │
            │                           │
            ▼                           │
    LLM call (architect)                │
    gets: goal, workspace scan,         │
          function map, interfaces,     │
          worklog, existing blueprints, │
          skill guidance                │
            │                           │
            ▼                           │
    Returns: blueprint, tasks[],        │
    setup[], validation[],              │
    interfaces, schema                  │
            │                           │
            ▼                           │
    Scheduler grafts:                   │
    setup → coders → execute →          │
    services → validators               │
            │                           │
            ▼                           │
    Each coder task becomes ────────────┘
    a shallow compute node
                    │
                    ▼
             ┌──────────────┐
             │ CODER FLOW   │
             │              │
             │ File exists? │
             └──┬───────┬───┘
                │       │
            NO  │       │ YES
                │       │
                ▼       ▼
           WRITE     EDIT MODE
           MODE
                        │
                ┌───────┴───────┐
                │               │
                ▼               ▼
          Has function     No function
          boundaries?      boundaries
          (EndLine > 0)
                │               │
                ▼               ▼
           TIER 1          TIER 2
           Surgical        One-shot
                │               │
    LLM gets:           LLM gets:
    - function map      - full file content
    - ONLY target       - function map
      function bodies     (if available)
    - goal + brief      - goal + brief
    - blueprint         - blueprint
                │               │
    LLM returns:        LLM returns:
    {edits: [           {edits: [...]}
      {start, end,      OR falls back to
       new_content}     {code: "full file"}
    ]}                        │
                │             │
                ▼             ▼
         SpliceFileEdits  SpliceFileEdits
         (bottom-to-top)  or os.WriteFile
                │             │
                └──────┬──────┘
                       │
                       ▼
              Log to worklog
              Write edit plan
```

## Function Scanner

`ScanFunctionMap(root, maxDepth)` in `internal/agent/utils.go`

- Walks the workspace, extracts function/class declarations from source files
- Pure Go, regex-based, 20+ languages supported
- Returns `FunctionMap` — `map[relPath][]FuncDecl`
- Each `FuncDecl` has: Name, StartLine, EndLine, Context, FilePath

### End-Line Detection

Three strategies based on language:

| Strategy | Languages | How |
|----------|-----------|-----|
| Brace counting | Go, JS, TS, Java, C, C++, Rust, PHP, Swift, Kotlin, Dart, Sh | Token-aware `{` `}` counter — skips strings, comments, template literals |
| Indentation | Python | Tracks indent level, function ends when indent drops back |
| Keyword | Ruby, Lua | Counts `def/class/if/do` vs `end` for depth |

### Language Pattern Map

`langDeclPatterns` — static map of file extension → compiled regexes. Group 1 captures the symbol name.

Supported: go, py, js, ts, tsx, jsx, mjs, vue, rb, rs, java, c, h, cpp, cs, php, swift, kt, sh, lua, dart

## Line-Range Splicer

`SpliceFileEdits(original, edits)` in `internal/agent/utils.go`

- Applies edits bottom-to-top (highest line first) so line numbers stay valid
- Validates: no overlapping ranges, all within file bounds
- `ApplyFileEdits(filePath, edits)` — file-level wrapper (read, splice, write back)

## Edit Tiers

### Tier 1: Surgical
- **When:** Function boundaries found (StartLine AND EndLine known)
- **What:** Only target function bodies sent to LLM, not the whole file
- **Matching:** Goal/brief text matched against function names to identify targets
- **Context:** Full function map as navigation, but only relevant function source code
- **Best for:** Large files where only 1-2 functions need changes

### Tier 2: One-shot
- **When:** No function boundaries found, or file is small
- **What:** Full file content sent to LLM
- **Context:** Function map if available, plus full file
- **Best for:** Small files, config files, files in languages without clear function boundaries

### Tier 3: Incremental (future)
- **When:** File too large for one-shot AND no function boundaries
- **What:** Walk through file in chunks, build boundary array incrementally
- **Not yet implemented**

## Edit Plans

When a coder makes edits (without a full architect blueprint), it writes a mini plan to `blueprints/edits/<filename>.edit.md`:

```markdown
# Edit: project/frontend/package.json

## Goal
Add missing "dev" script for Vite

## Edits
- Lines 4-6: "scripts": { "dev": "vite", "build": "vite build" }
```

## Call-Site Analysis (not yet integrated)

`FindCallSites(workspace, funcName, fm)` in `internal/agent/utils.go`

Purpose: when a function's signature changes, find all callers in the workspace that need updating.

- Greps workspace files for `funcName(`
- Maps each hit to the containing FuncDecl via line ranges
- Returns caller FuncDecls for the coder to update

Currently exists as a utility function but is not called from the coder flow.

## Files

- `internal/agent/compute.go` — computePlan (architect), computeCode (coder), edit mode detection
- `internal/agent/utils.go` — ScanFunctionMap, SpliceFileEdits, FindCallSites, FormatFunctionMapForPrompt
- `internal/agent/prompts.go` — baseComputeArchitectPrompt, baseComputeCoderPrompt (with EDIT mode)
- `internal/agent/codescan_test.go` — tests for scanner, splicer, call-site analysis
