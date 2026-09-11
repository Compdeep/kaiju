# Which models need longer than the deadline, and how much longer

_Measured 2026-09-10, from two sources: the planner-prompt bench in
`docs/bench/planner_speed_bench.py` (54 runs across the live OpenRouter
catalogue, raw results in `planner_speed_results.json`, spend $0.48) and 2,577
real calls on one deployment, read out of the prompt debug log by
`docs/bench/live_pace.py`._

## Why a per-model allowance exists

A round has a deadline because `max_tokens` bounds the reply and not the wait: a
model that reasons before answering can spend an unbounded amount of time doing
it. The deadline is 120 seconds at the ordinary effort — see
`agent/round_budget.go` — and a call that passes it is asked again with thinking
off, which costs a whole second call and produces a worse answer than waiting
would have.

One number cannot fit every model. Marking the ones that need longer is
`models.Info.Pace`: `slow` is given half again as long, `very slow` twice as
long. Absent is ordinary, which is what every other entry says.

Both deadlines read it and they move together — the round deadline here and the
request deadline in `agent/llm/client.go`. Widening one alone means the call is
cut by whichever was left, and the error then names the wrong one.

## What the bench said, and why it was not enough

One prompt — the executive's real 51KB system prompt with the plan tool forced —
three samples on each of the models a deployment might plan with. Against gpt-5
at 77.3 seconds as the baseline:

- `qwen/qwen3-32b` — 386.1s, five times the baseline.
- `openai/gpt-5` — 85.5s, 77.3s, 76.0s.
- `z-ai/glm-5.3` — 81.8s.
- `moonshotai/kimi-k3` — 80.9s.
- `moonshotai/kimi-k2.5` — 54.8s.
- Everything else below 54s, down to 1.6s for gemini-3.5-flash-lite.

Read alone, that says a flat 120 seconds fits 51 of 52 models and the one it
does not is beyond any allowance worth giving. That is how this was first
decided, and it was decided on a median.

## What live traffic said

The same deadline, against real runs of every stage rather than one prompt:

- `moonshotai/kimi-k2.6` — 26 calls, median 22.4s, 90th percentile 132.5s,
  longest 155.4s. **5 of 26 passed the deadline.**
- `z-ai/glm-5.3` — 22 calls, median 37.0s, 90th percentile 112.7s. 2 passed it.
- `qwen/qwen3.6-35b-a3b` — 713 calls, median 2.5s. 4 passed it.
- `openai/gpt-4.1-mini` — 426 calls, median 0.9s, longest 9.8s. None.

kimi-k2.6 has an ordinary median and a tail well past the deadline, and only the
tail is cut off. A bench that reports a median cannot see that, which is why the
first reading of this was wrong.

## What is marked, and on what evidence

- `moonshotai/kimi-k2.6` → `slow` (180s at the ordinary effort). Live: 5 of 26
  calls at or past the deadline, 90th percentile 132.5s.
- `qwen/qwen3-32b` → `very slow` (240s). Bench: 386.1s on the planner prompt,
  five times the baseline. Twice is not enough for it and no allowance would be;
  what twice buys is the difference between cutting it off at two minutes and
  cutting it off at four, and the second is a plan more often.

Nothing else is marked. `z-ai/glm-5.3` is the closest call and was left
ordinary: 22 calls is a thin sample, and its 90th percentile sits under the
deadline rather than over it.

## Re-running this

The bench needs an OpenRouter key, read from the local kaiju config, and costs
about fifty cents. `live_pace.py` costs nothing and reads
`/tmp/kaiju-prompts`, so it is worth running first — it measures the models a
deployment actually uses, on the prompts it actually sends.

A model is worth marking when its 90th percentile passes the deadline, not when
its median is high. Mark the smallest step that covers the tail: this lengthens
a wait for every call the model makes, and a run that waits four minutes for a
model that needed two has spent the time either way.
