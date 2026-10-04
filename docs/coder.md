# The Coder

The coder is the LLM call that writes file content. It is one stage with one job:
given a goal and a file, say what should change. It never touches the filesystem —
the engine does that — and it never decides what work exists, which is the planner's
job and the architect's.

It does two different things, and only one of them was designed for:

- **Write a script and run it.** The coder emits code plus an `execute` command, and
  the scheduler grafts a bash node to run it. This is what compute was built for and
  it works.
- **Change a file in an existing codebase.** This was added to the generator with a
  single boolean, and every problem below came from that.

## The shape

```
  PLANNER  (LLM)
    │   edit_file{ task_files, goal }
    ▼
  ENGINE
    │   resolves the path, stats the file, reads it numbered
    │   → CoderRequest: goal, file, exists, lines, head
    ▼
  CODER  (LLM)
    │   one call. Answers with a declared result.
    ▼
  ENGINE
    │   applies it — the only write in the node
    │   measures the delta itself
    ▼
  ENGINE
    │   worklog line, node state
    ▼
  REFLECTOR  (LLM)
        continue / replan / conclude
```

The coder authors the change as text. The engine finds that text in the real file
and replaces it. Path resolution, structural validation of JSON and YAML, and the
write itself all stay with the engine, because those are the things an LLM cannot
do safely for itself.

## What it is given

`CoderRequest` (`agent/coder_protocol.go`). Goal and file come from the caller.
`exists`, `lines` and `head` come from the engine, because no caller supplies them
and the coder must not invent them.

| Field | From | |
|---|---|---|
| `goal` | caller | what to do |
| `file` | caller | the one file this call may change |
| `exists` | engine | whether there is anything there |
| `lines` | engine | how long it is |
| `head` | engine | its first lines, numbered |
| `context` | caller | outputs wired from earlier steps |
| `prior` | engine | what earlier coders did to this file |
| `brief` | architect | notes for this work item |
| `interfaces` | architect | types and APIs to implement against |
| `structure` | architect | the project tree |

The engine-filled three are the point. Before them, every caller filled a different
subset of a `map[string]any` — `edit_file` seven keys, the architect eleven, a
plan's compute step four — and none of them carried the file's contents. One coder
was handed `Available Data: None`, asked to preserve an Express server it had never
seen, and wrote its own idea of one.

A file under `coderHeadLines` (400) arrives whole. A longer one says how many lines
were withheld and that rewriting it would discard everything below the cut.

## What it returns

`CoderResult`. Status leads, so the coder states what happened before describing it.

| Status | Carries | Node becomes |
|---|---|---|
| `edited` | `edits` | resolved |
| `created` | `code` | resolved |
| `no_change` | `summary` | **empty** |
| `blocked` | `blocked{needs, from}` | **failed** |

`Validate` refuses a status that contradicts its payload — `edited` with no edits
used to write nothing and report success.

`summary` is the coder's own words. The delta beside it is the engine's measurement:
a model reporting "12 lines changed" is inventing a number the engine already knows.

### blocked

The field whose absence caused the damage. `code` was required, so a coder with
nothing useful to contribute still had to contribute a file.

Declining is a complete answer and the run recovers from it: the node fails, the
reflector reads the reason and plans differently. That path works — it handled nine
simultaneous failures correctly in one run and replanned with the right paths. What
it cannot act on is a coder that succeeded at the wrong thing.

`from` says who can unblock it, and the split matters: "you did not tell me what to
preserve" (`planner`) and "I could not read the file" (`environment`) want different
responses. As one undifferentiated error string the reflector guesses, and it has
guessed wrong — a raw "no such file or directory" twice sent it to investigate a
timing problem that did not exist.

## Edits

An edit names both where and what:

```json
{ "lines": [45, 52],
  "old_content": "</body>",
  "new_content": "<script src=\"/docs/backdrop.js\"></script>\n</body>",
  "why": "the backdrop scripts belong at the end of body" }
```

Neither alone is enough. Text cannot tell two identical strings apart: on `</body>`
in a file with two of them, `ApplyEdits` replaced the first and reported success.
Line numbers go stale: two edits in one set, and the first adding a line puts every
later number out by one.

Together each covers the other. The lines say which occurrence. The text is the
check — if it is not at those lines the file is not what the coder thought, so the
edit is refused and the error carries both what was expected and what is there.

Without `lines`, text appearing more than once is refused rather than resolved by
position, and the error says how many matches there were.

Positions are resolved against the file **as the coder saw it**, never against a
partly-edited version. Every positional edit is checked against the original before
anything is applied, overlapping ranges are refused, and the set is then applied
bottom-up — each edit only moves lines below itself, and those are already done.
Text-only edits carry no positions and follow, in the order given.

That ordering is not cosmetic. One real set replaced 12 lines with 14 at line 61 and
then named line 122 — correct for the file it was shown, two lines stale by the time
it was checked. It was refused, the coder was told the file was not what it thought,
and it retried the same correct edit twice. The more edits a coder makes, the more
certain that becomes.

The whole set is atomic: one bad edit fails all of them and the file is untouched.

## Why edits rather than whole files

A rewrite needs the universe; an edit needs the part being changed. Three
consequences:

A file too long to show cannot be rewritten safely, because anything not reproduced
is gone. `main.ts` went from 2,458 bytes to 2,953 and lost `dotenv`, the postgres
connection, compression, and four of its five route mounts.

`file_read` caps at 500 lines by default. A coder whose job is to return the whole
file will faithfully write back a truncated one.

And a no-op becomes visible. A coder that rewrites a file identically is
indistinguishable from one that did the work — `14-s11.html` was rewritten four
times, each logging `OK: wrote`, and 32 em-dashes became 31.

## How the engine applies it

`ApplyFileEdits` for `edited`, `OverwriteFile` for `created`, both through
`commitFile`, which parse-validates structured files before anything reaches disk.
Then it compares the result to what was there:

- changed → `OK`, with the measured delta
- unchanged → `no_changes`, which `computebody.go` turns into an **empty** envelope
  rather than a success, and `NO_CHANGE` in the worklog

That last line is what the next coder reads. `OK: wrote` is what told four passes in
a row that the job was already done.

## Reaching it

All three routes funnel through `runCompute` → `computeCode`, so one implementation
serves them:

| Caller | Mode | |
|---|---|---|
| `edit_file` | shallow | "an LLM should touch this named file". `task_files` required and validated at schedule time |
| architect (`computePlan`) | deep → shallow children | plans the work, then one coder node per file, grafted in parallel |
| a plan's `compute` step | shallow | analytical scripts. `task_files` is refused here on purpose |

`edit_file` forcing shallow is deliberate, not a gap. The architect decides what
files should exist; it is the wrong stage for understanding one that already does.
It also exists because `task_files` was once optional on compute and the coder
hallucinated filenames, clobbering a file.

## Known gaps

**The coder cannot look.** It sees the file it was given and nothing else. "Does
this edit make sense against `backend/objectives`" is unanswerable, and the planner
cannot supply the answer because it has not read anything either. The intended fix
is a bounded read-only loop — `file_read` with an offset, `file_list` — ending in
the same declared result, with the write staying terminal so nothing is half
applied and declining stays free.

**`execute` leaks into the editing path.** `builtin_edit_file.go` documents that it
does not run scripts, and `compute.go` attaches `execute` to an edit result anyway.
A top-level graft would then run it.

**Prior edits are not yet wired.** `CoderRequest.Prior` is declared and nothing
fills it, so a later coder cannot see what an earlier one did to the same file.

## Code

| | |
|---|---|
| `agent/coder_protocol.go` | `CoderRequest`, `CoderResult`, `Validate`, `coderFileFacts` |
| `agent/compute.go` | `computeCode` — prompt assembly, the call, status dispatch, apply |
| `agent/stage_schemas.go` | `coderSchema` — the result shape the model is offered |
| `agent/utils.go` | `EditOp`, `ApplyEdits`, `ApplyFileEdits`, `commitFile` |
| `agent/prompts.go` | `baseComputeCoderPrompt` |
| `agent/computebody.go` | status → envelope |

See also [graph.md](graph.md) for how a compute node sits in a run, and
[tools.md](tools.md) for `edit_file`'s place among the tools.
