# Pre-integration evals

These are **not** run by `go test ./...`. They spend real LLM tokens, take
minutes, and are meant to be invoked when a developer considers a day's work
shippable — think of them as a pre-integration gate that sits between unit
tests and actual integration.

Three suites are exposed through one dispatcher:

| Suite      | What it exercises                                                     | LLM calls / run     |
|------------|-----------------------------------------------------------------------|---------------------|
| `editor`   | One-shot coder edits against a fixture corpus (editor layer in isolation). | 1 per scenario      |
| `pipeline` | Full kaiju end-to-end on a broken multi-file backend; asserts the debug cycle fans the fix across all affected files. | ~planner+coders     |
| `holmes`   | End-to-end fixes on small broken projects; pass/fail comes from running the patched artefact (python3/npm/jq). | ~full cycle         |

## Prerequisites

```sh
go build -o kaiju ./cmd/kaiju
```

`kaiju.json` must exist in the working directory with a working LLM API key
(or `${ENV}` references the dispatcher can resolve).

## Dispatcher

```sh
# editor — core tier only (~17 scenarios, fast smoke gate)
go run ./tests/eval/cmd -kind=editor -tier=core

# editor — the whole corpus (~50 scenarios, costly)
go run ./tests/eval/cmd -kind=editor -tier=all

# editor — one fixture family
go run ./tests/eval/cmd -kind=editor -only=python

# pipeline — multi-file fan-out test
go run ./tests/eval/cmd -kind=pipeline

# holmes — one specific scenario
go run ./tests/eval/cmd -kind=holmes -only=fix_json_comma

# everything (editor uses whatever -tier was passed, default "all")
go run ./tests/eval/cmd -kind=all -tier=core
```

Common flags:

- `-kaiju=<path>` — kaiju binary (default `./kaiju`)
- `-config=<path>` — `kaiju.json` (default `./kaiju.json`)
- `-timeout=<dur>` — per-suite timeout (0 = suite default)
- `-keep` — keep the tmp workdirs after pipeline/holmes runs
- `-dry` — editor only; parse scenarios and exit without calling the LLM

## Tiering (editor only)

Scenarios in `corpus/**/*.scenarios.jsonl` can be tagged `"core": true`. When
invoked with `-tier=core`, only tagged scenarios run; otherwise everything
runs. The core set is intentionally small (~17) — one representative
scenario per language/file-type family.

## Layout

```
tests/eval/
├── cmd/                  # dispatcher — `go run ./tests/eval/cmd`
├── editor/
│   ├── eval.go           # package editor, Run(opts)
│   └── corpus/           # fixture files + *.scenarios.jsonl
├── pipeline/
│   ├── eval.go           # package pipeline, Run(opts)
│   └── fixture/          # broken kaiju_saas backend
└── holmes/
    ├── eval.go           # package holmes, Run(opts)
    └── fixtures/         # one subdir per scenario
```

Each `Run(opts)` returns `0` on pass, non-zero on fail. The dispatcher
`os.Exit`s on that value, so CI wrappers can key off the exit code.
