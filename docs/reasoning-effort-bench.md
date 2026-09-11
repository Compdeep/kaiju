# Which models act on a reasoning effort, and which only accept one

_Measured 2026-09-09 against the live OpenRouter catalogue. Total spend $1.67._

_Harness and raw runs are in `docs/bench/`: `effort_bench.py` makes the calls,
`effort_verdict.py` applies the rule below to `effort_bench_results.json` and
`effort_bench_pass2.json`, and `budget_bench.py` is the token-budget half. They
are kept in the repo rather than in a scratch directory because the previous
bench here was not, and `router-model-bench.md` now says so at the top. Re-run
after any catalogue refresh: the OpenRouter key is read from the local kaiju
config, and `RUNS` and `OUT` are environment variables._

## Why this had to be measured

Every provider accepts `reasoning.effort` and `reasoning.max_tokens`. None
errors on either. A model that does nothing with the value answers exactly like
one that acts on it, so asking the provider tells you nothing and a control
offered on the strength of the parameter existing is a control that appears to
work: the setting saves, the config file shows it, and the run is unchanged.

So `models.Info.ReasoningEfforts` and `ReasoningBudget` are measurements, and
`agent.applyReasoningBudget` sends a value only where the measurement says the
model acts on it.

## What the vocabulary turned out to be

Six values, not three: `minimal`, `low`, `medium`, `high`, `xhigh`, `max`. The
field was built with `low/medium/high`, which is what the OpenAI wire documents
and what a scale usually is. It could not express the catalogue:

- `z-ai/glm-5.2` takes `xhigh` and `high` and neither `low` nor `medium`.
- `google/gemini-3.5-flash-lite` takes `minimal`, at which it reasons not at all.
- Nine models take `max`, and on one of them it means something dangerous — see
  kimi-k3 below.

`none` is not among them. Not thinking at all is the reasoning switch's
question, and `agent.ParseReasoningEffort` refuses it.

## Method

- One prompt, a two-line arithmetic comparison, `temperature 0`, `max_tokens
  16000` so nothing is cut off by the reply budget. Bounded on purpose: a prompt
  that invites open-ended reasoning makes the spend depend on the prompt as much
  as on the setting.
- Each effort the OpenRouter catalogue claims the model supports, three runs,
  five for anything ambiguous. The measure is `usage.completion_tokens_details.
  reasoning_tokens`.
- One run saying nothing at all, to see where the model's own default sits.

**The verdict rule.** A value earns its place by being distinguishable from the
weakest setting the model takes: its runs must not overlap that setting's at
all. Non-overlap with five runs each is a real gap, it assumes nothing about how
reasoning length is distributed — just as well, since kimi-k3's is a spike at
the token cap — and it is judged against the weakest rather than the next step
up, because the steps are not evenly spaced and adjacent pairs often overlap
where the ends do not.

Two earlier rules were wrong and are worth recording:

- **A ratio between the largest and smallest median.** It divides by zero on
  exactly the strongest result in the set: `claude-fable-5.1` reasons for **zero**
  tokens at `low`, `medium` and `high`, and 153 at `xhigh`. The first pass filed
  that under "no usable reading".
- **A ratio has no direction.** `deepseek/deepseek-v4-flash-0731` reasons for 499
  tokens at `low` and 84 at `high` — reproducibly, five runs, no overlap. By
  ratio it is one of the most responsive models in the set. Offered as a control
  it would do the opposite of what its label says.

## Result

**Acts on it — 17 models.** Median reasoning tokens:

- `anthropic/claude-opus-5` — low 100, medium 116, high 113, xhigh 115, max 133
- `anthropic/claude-sonnet-5` — low 122, xhigh 170, max 446
- `anthropic/claude-opus-4.8` — low 114, xhigh 167, max 183
- `anthropic/claude-sonnet-4.6` — low 127, medium 143, high 229, max 259
- `anthropic/claude-fable-5.1` — low 0, xhigh 153, max 158
- `deepseek/deepseek-v4-pro-0813` — low 123, high 187
- `google/gemini-3.5-flash-lite` — minimal 0, low 204, medium 352, high 541
- `google/gemini-3.7-flash` — low 145, medium 343, high 453
- `google/gemini-3.8-flash` — low 137, medium 365, high 424
- `ibm-granite/granite-4.2-8b` — low 70, high 846
- `meta/muse-glimmer-30b` — low 131, medium 193, high 338, xhigh 366
- `moonshotai/kimi-k3` — low 95, high 111, max 16001
- `openai/gpt-oss-120b` — low 56, medium 102, high 261
- `openai/gpt-oss-20b` — low 52, medium 89, high 339
- `qwen/qwen3.8-27b` — low 65, medium 244, xhigh 391
- `z-ai/glm-5.2` — high 306, xhigh 470
- `z-ai/glm-5.3` — low 71, high 92, max 107

Where a claimed value is missing from that list it was dropped for producing the
same thinking as the weakest one: two labels for one behaviour is a control with
two names. `claude-fable-5.1`, `claude-opus-4.8` and `claude-sonnet-5` lost
`medium` and `high` this way, and `deepseek-v4-pro-0813` lost `max`.

**Accepts it and does nothing — 5 models.** `qwen/qwen3.8-2.4t-a95b` returned a
median of 136 tokens at `low`, `medium` AND `xhigh`. `thinkingmachines/
inkling-small` and `z-ai/glm-5.3-flash` vary more between two runs of the same
setting than between settings. `meituan/longcat-2.0`, `qwen/qwen3.7-flash` and
`qwen/qwen3.8-flash` claim no efforts at all.

**Runs backwards — 2 models.** `deepseek/deepseek-v4-flash-0731` (low 499, high
84) and `deepseek/deepseek-v4-flash-vision-exp` (low 359, high 97). Nothing is
recorded for these: no control is better than a reversed one.

**Not measured for effort — 1 model.** `nvidia/nemotron-3-super-120b-a12b` was
rate-limited upstream on most attempts across four sessions; enough calls landed
to settle its budget behaviour but not its efforts. It claims `medium` and `low`.

Rate limiting shaped what could be measured here generally: `qwen/qwen3.8-flash`
and this model returned HTTP 429 from the upstream provider on roughly half of
all attempts, which is why the budget runs above are spaced 45 seconds apart.

### kimi-k3 at `max` reasons until the reply budget is gone

`moonshotai/kimi-k3` spent 16,001 reasoning tokens at `max` — the harness's
entire 16,000-token allowance, `finish_reason: length`, twice in three runs. It
does the same saying nothing at all, which is its default. This is the failure
that prompted the whole feature: a reasoning model given a budget it can spend
entirely on reasoning returns an empty reply, and the run reports "empty
response" seventeen minutes later.

`max` is recorded for it, because it is a real and distinguishable setting, and
an operator choosing it should know it means "think until the budget is gone".

## The token budget

Asked at 128 and at 4,096, three runs each.

### The arithmetic prompt was the wrong prompt for this half

A budget only shows as a budget where it **binds**. Several of these models
reason for about 130 tokens on the arithmetic question unprompted, so a 128 ask
cannot be told apart from their own default, and a 4,096 ask is never reached.
`qwen/qwen3.8-flash` returned exactly 128 at the low ask and was filed as
ignoring it, because 128 was not meaningfully below the 132 it wanted anyway.

The budget runs were repeated on a five-house logic puzzle, which these models
reason about for thousands of tokens. There the answer is unmistakable.

**Honoured — 2 models.**

- `qwen/qwen3.7-flash` — 8,000 and 7,700 tokens saying nothing; **exactly 256**
  twice when 256 was allowed; 4,096 and 3,877 when 4,096 was.
- `qwen/qwen3.8-flash` — 8,000 saying nothing, **exactly 4,096** when 4,096 was
  allowed, and exactly 128 when 128 was on the easier prompt. Three clips at
  three different asks, and its own natural spend whenever the ask did not bind.

**Not honoured — 5 models.** `anthropic/claude-opus-5` spent 113 tokens whether
asked for 128 or 4,096. `claude-sonnet-5` and `claude-fable-5.1` likewise, and
`meituan/longcat-2.0` returned 315 either way despite the catalogue claiming
`supports_max_tokens`. `nvidia/nemotron-3-super-120b-a12b` was asked for 256 and
spent 1,781 and 2,097 — seven times the allowance.

### This removed a claim the catalog was already making

`claude-opus-5`, `claude-sonnet-5` and `claude-fable-5.1` carried
`reasoning_budget: true` on the grounds that Anthropic takes a budget natively,
as `budget_tokens`. On the OpenRouter wire they do not, and OpenRouter's own
per-model metadata agrees, listing no `supports_max_tokens` for any of them. The
three are now false.

**This is a statement about the wire, not only the model.** Every entry in
`models.json` is `"provider": "openrouter"`, so the OpenRouter wire is the one
that matters here. A deployment pointed at `api.anthropic.com` directly speaks
the native wire, where `budget_tokens` is the documented parameter and would very
likely be honoured. That could not be measured: there is no Anthropic key on
this machine. If kaiju grows native-Anthropic lanes, this is the first thing to
re-measure.

## Caveats

- **One prompt.** Adjacent steps that did not separate here may separate on
  harder work. The values recorded are those that separated from the floor, which
  is the conservative direction: a model may act on more values than are listed,
  and should not act on fewer.
- **A moment in time.** These are hosted models behind a router that changes
  which upstream serves a request. `deepseek-v4-pro-0813` gave low 110 / high 220
  in one session and low 123 / high 187 in another.
- **Nothing here is inferred from the family.** `deepseek-v4-pro-0813` acts on
  effort and `deepseek-v4-flash-0731` acts on it backwards. `glm-5.3` acts on it
  and `glm-5.3-flash` does not. This is why `Verified` exists for `ToolCallOK`,
  and the same rule applies here.
