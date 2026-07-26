# Router / Preflight model quality bench

_Recorded 2026-07-26. Harness + raw results: `scratchpad/preflight_bench.py`,
`preflight_bench_results.json`._

## TL;DR

The kaiju router (the "does this turn need the agent?" decision) was defaulting to
`openai/gpt-5-mini` and **silently under-escalating** — live queries that clearly
needed tools (news, live data, multi-step) were staying in the tool-less chat lane.

Root cause: **the router is a reasoning model running inside a 16-token budget.**
The route call is a forced tool-call capped at 16 output tokens; a reasoning model
(gpt-5-\*) spends that budget on hidden reasoning and emits **no tool call**, so
`routeQuery` hits its fail-safe and returns `"chat"`. Every under-escalation was
that fail-safe firing.

We benched 8 candidate models against 10 gold-labelled queries using the **real**
embedded ROUTE + PREFLIGHT prompts and kaiju's exact tool schemas. Winner:

> **`openai/gpt-4.1-mini` — 100% route accuracy, 100% budget-fit at 16 tokens, ~700ms.**
> Now the default `RouteModel`. Fallback: `qwen/qwen3-30b-a3b-instruct-2507` (also
> 100%, no OpenAI dependency, but ~3.8× slower).

## What "the router" is

Two classifiers sit in front of the agent (`internal/agent/preflight.go`):

1. **ROUTE** (`routeQuery`) — the escalation gate. System prompt = `prompt.Route`,
   forced `route` tool (`mode` ∈ chat|meta|investigate), `temperature 0`,
   `max_tokens 16`, run on the **pinned route model** (`agent.route_provider` /
   `agent.route_model`). **Fails safe to `"chat"`** on any error, refusal, or
   missing tool call. This is the decision under test.
2. **PREFLIGHT** (`classifyInvestigate`) — runs only *after* ROUTE says
   `investigate`. Forced `submit_preflight` tool, `max_tokens 256`. Emits
   `mode / intent / required_categories / context / compute_mode` to shape the DAG.

Note the two prompts define "chat" oppositely: ROUTE treats chat as the *default*
(escalate only for external/live/action); PREFLIGHT treats investigate as the
default (chat = greetings only), because by the time PREFLIGHT runs, escalation is
already decided. Escalation quality therefore lives entirely in **ROUTE**.

## Method

- **Harness**: `scratchpad/preflight_bench.py`. Reads the live embedded prompts
  from `internal/agent/prompt/prompts.md`, uses kaiju's exact `route` and
  `submit_preflight` tool schemas, and calls OpenRouter directly (no full-DAG
  runs). It replicates `routeQuery` faithfully, including fail-safe-to-chat.
- **Phase A (ROUTE / mode)**: 8 models × 10 queries × **5 runs @ 16 tokens**
  (production budget) + 1 run @ 512 tokens (generous, to separate "starved" from
  "wrong").
- **Phase B (PREFLIGHT depth)**: the investigate-shaped queries × 2 runs @ 256
  tokens, scoring `required_categories`, `compute_mode`, and identifier
  preservation in `context`.
- `temperature 0` throughout; majority vote across runs for the per-query verdict.

## The gold query set

Ten queries spanning complexity and vagueness, deliberately including **traps in
both directions** — questions that *sound* actiony but are answerable from
knowledge (should stay chat), and terse/vague questions that quietly need live
data (should escalate).

| # | query | gold | why it's here |
|---|---|---|---|
| Q1 | Explain the difference between TCP and UDP. | chat | plain explainer — must not over-escalate |
| Q2 | How do I set up nginx as a reverse proxy with HTTPS? | chat | **trap**: imperative how-to, still knowledge |
| Q3 | Find recent news about the JWST and cite the source URLs. | investigate | live + citations (the original bug) |
| Q4 | Compare current GPU instance pricing across AWS, GCP and Azure and say which is cheapest. | investigate | multi-source live + rank |
| Q5 | What tools do you have access to and what can you actually do? | meta | capability / self-referential |
| Q6 | What's the latest with the Fed and interest rates? | investigate | **vague** + implicit recency |
| Q7 | Tell me about black holes and where the event horizon sits for the different types. | chat | broad but timeless physics |
| Q8 | Calculate the mean center of gravity of all the planets right now relative to Earth. | investigate | live ephemeris + compute (`compute_mode: shallow`) |
| Q9 | 12 coins, one heavier, a balance scale — optimal strategy, and the max coins it generalises to. | chat | **trap**: "calculate/optimal/max" bait, pure reasoning |
| Q10 | What's the weather in Tokyo right now? | investigate | terse single live lookup |

## The rubric

| dimension | what it measures |
|---|---|
| **mode-acc** | majority-vote ROUTE decision vs gold, per query. The escalation quality. |
| **fit@16** | did it emit a valid tool call within the 16-token production budget? *The great filter* — anything that fails here silently becomes `chat`. |
| **consistency** | same answer across 5 runs at temp 0. |
| **latency** | mean ms per route call. |
| **@16 vs @512** | separates *starved* (right when given room) from *wrong* (bad even unstarved). |
| **cats / compute / ctx** (Phase B) | categories recall, `compute_mode` accuracy, verbatim identifier preservation in `context`. |

## Phase A — ROUTE / mode (production: 5 runs @ 16 tokens, fail-safe = chat)

| model | mode-acc | fit@16 | consistency | latency | notes |
|---|---|---|---|---|---|
| **gpt-4.1-mini** | **100%** | **100%** | 100% | 696ms | perfect on every trap *and* escalation |
| qwen3-30b (baseline) | 100% | 100% | 100% | 2645ms | perfect, but ~3.8× slower |
| gemini-2.5-flash-lite | 70% | 100% | 100% | 617ms | over-escalates Q2, Q7, Q9 |
| llama-3.1-8b | 70% | 70% | 100% | 452ms | 30% starve; misses Q6, Q10 |
| gpt-5-mini | 40% | **10%** | 100% | 1168ms | starved → silent chat |
| gemma-3-12b | 40% | **0%** | 100% | 582ms | starved at 16 tokens |
| qwen3-14b | 40% | **0%** | 100% | 478ms | thinking variant, never fits |
| gpt-5-nano | 40% | **0%** | 100% | 1119ms | starved *and* wrong |

The 40%-accuracy cluster is an artefact of the fail-safe: with `fit@16 ≈ 0`, every
query collapses to `chat`, so only the four chat-gold queries "pass" by accident —
and every investigate query is missed. That is exactly the production failure.

**Winner, per query:** gpt-4.1-mini got Q2/Q7/Q9 → `chat` (the three traps that
fooled gemini/llama), Q3/Q4/Q6/Q8/Q10 → `investigate`, Q5 → `meta`. 10/10.

### Reasoning-model diagnosis (@16 vs @512)

Raising the token cap would **not** rescue the gpt-5 family:

| model | fit@16 | unstarved (@512) acc | verdict |
|---|---|---|---|
| gpt-5-mini | 10% | 70% | starved **and** mediocre when unstarved |
| gpt-5-nano | 0% | 50% | starved **and** wrong |

gpt-5-mini classified the JWST query as `chat` even with 600 tokens to reason —
wrong regardless of budget.

## Phase B — PREFLIGHT depth (categories / compute_mode / context)

| model | categories-recall | compute_mode-acc | context-keys-preserved |
|---|---|---|---|
| **gpt-4.1-mini** | **100%** | **100%** | **100%** |
| qwen3-30b | 90% | 100% | 100% |
| gemini-2.5-flash-lite | 100% | 67% | 100% |
| llama-3.1-8b | 0% | 67% | 100% |
| gemma-3-12b | 100% | 67% | 100% |
| qwen3-14b | 40% | 100% | 40% |
| gpt-5-mini / nano | 0% | 83% | 0% |

gpt-4.1-mini is best on the executor/preflight role too: it flagged Q8 (planetary
center-of-gravity) `compute_mode: shallow` while keeping Q9 (coins puzzle) at `""`,
and preserved every identifier verbatim.

## Models that couldn't be benched

- `qwen/qwen3-8b` → HTTP 400 (`invalid_parameter`): the OpenRouter provider
  rejects the forced-tool-call parameters. Unusable as a tool-call router.
- `qwen/qwen3-14b` → no tool call at all (thinking variant). Included in the
  scorecard only to show it fails; not viable.

## Decision

- **Router default → `openai/gpt-4.1-mini`** (`internal/config/defaults.go`,
  `RouteModel`). Live in `kaiju.config.json` and re-labelled in the model catalog
  (`internal/api/config_handlers.go`: gpt-4.1-mini = "router · recommended",
  gpt-5-\* = "reasoning · not for router"). Configurable per-deployment in the
  Settings UI router picker.
- **Fallback → `qwen/qwen3-30b-a3b-instruct-2507`** — also 100%, no OpenAI
  dependency, self-host-friendly, just slower.
- **Rejected**: all gpt-5-\* (reasoning starvation), the small Qwens (unavailable /
  thinking), gemma-3-12b (starves at 16 tokens), gemini-flash-lite & llama-3.1-8b
  (70%, over/under-escalate).

## Future option: a dedicated classifier (BERT)

The ROUTE decision is a 3-way classification — architecturally a perfect fit for a
fine-tuned encoder (DistilBERT), which would be faster, cheaper, and more
deterministic than any generative LLM. It doesn't fit today because (a) it can't
emit a tool call / the generative PREFLIGHT fields, (b) it needs labelled training
data — this gold set is a seed — and (c) it isn't on OpenRouter, so it would be
self-hosted (the `mcs-worker`, already running GLiNER, is the natural home).
Revisit only if route volume makes LLM latency/cost bite.

## Reproducing

```bash
cd scratchpad
python3 preflight_bench.py      # ~576 calls, 8-way concurrent, ~3 min
```

The harness pulls the prompts live from
`/home/sites/kaiju/kaiju/internal/agent/prompt/prompts.md`, so it re-tests whatever
ROUTE/PREFLIGHT prompt is current. Edit `MODELS` / `QUERIES` to extend.
