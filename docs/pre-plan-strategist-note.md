# Future work: a free-reasoning "strategist" step before the planner

_Design note, not yet built. Captured 2026-07-26 from a discussion on putting
high-quality (thinking) models on the reasoning parts of the agent loop._

## The idea

Insert a dedicated **strategist** step *before* the executive/planner:

```
STRATEGIST (thinking model, NO tool call, free-form)  →  PLAN (executive, tool call)  →  tools → reflect → aggregate
```

The strategist is a thinking model with **no tool-call constraint** — it just
writes *"here's how I'd approach this problem"* (the strategy, the likely pitfalls,
what data is needed). Its output is fed into the planner as context, so the planner
converts a well-reasoned strategy into concrete `plan()` steps instead of reasoning
and formatting JSON in the same breath.

## Why it might help

When the planner is a **forced `plan()` tool call**, its reasoning is shaped by
having to emit JSON. A free-form reasoning step *before* it can think more deeply
and freely about the approach, unconstrained by the output format.

## Why it might NOT be needed (try this first)

In kaiju, **reasoning and tool-calling are not separate models** — the planner
reasons *and* emits the tool calls in one step, and the tools themselves are
deterministic (`bash`, `web_search`). So "thinking model to reason, tool model to
call tools" mostly collapses into **"put a thinking model on the reasoning calls."**
A **thinking planner** reasons in its own thinking channel and *then* emits the
`plan()` JSON — which already gives most of the strategist's benefit in one call.
The planner has a 4096-token budget, plenty of room for a thinking model (the
small-budget starvation that broke the router/executor does **not** apply here).

So: **add the strategist only if a thinking planner's plans still come out shallow.**

## The reasoning points in the loop (where "thinking" belongs)

kaiju already runs a think → tool → think loop:

```
PLAN (think: strategy) → TOOLS (gather) → REFLECT (think again) → [replan → tools] → AGGREGATE (think over all data)
```

The chicken-and-egg — *you need data before you can reason deeply* — is resolved by
**where** the heavy reasoning lands: **after** data, at **reflect** and
**aggregate**. Pre-data thinking (plan) is necessarily lighter (strategy, not
substance), and that's correct.

The three reasoning points and their thinking-model status:

| point | thinking model? | status |
|---|---|---|
| **aggregate** (final answer) | yes — open generation, no tools | **done** (the answer lane) |
| **plan** (executive) | yes — 4096-tok budget, tool-with-thinking works | selectable now (reasoning picker allows `tools:true` incl. thinking) |
| **reflect** (continue/replan/conclude) | wants thinking, but… | **needs a lane split** — the reflector currently rides the executor (non-thinking) lane, shared with preflight; splitting it onto its own lane (like the aggregator split) is the prerequisite |

## Recommendation

1. Make the planner a strong **thinking model** and measure plan quality (verify it
   reliably emits `plan()` at 4096 tok — the router-model bench harness tests this,
   just bump the budget).
2. Keep the **aggregator** on a thinking model (answer lane, already possible).
3. If reflect-stage decisions need more depth, do a **reflector lane-split** so it
   can take a thinking model without breaking preflight.
4. Only if plans are *still* shallow after (1), add the dedicated **strategist**
   step above.

## Related

- Small-budget forced tool calls starve thinking models — see
  `docs/router-model-bench.md`. That's why route + executor lanes are non-thinking,
  but plan/aggregate (big budget / no tools) are fine for thinking models.
- The answer-lane split (aggregator off the planner lane) is the pattern a
  reflector-split would follow.
