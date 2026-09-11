# Model Call Limits and Reasoning Control

This document describes how Kaiju controls model calls: output token limits, reasoning budgets, execution timeouts, model-specific adjustments, and recovery from incomplete responses.

*kaiju · main · as of `3a875b6`*

## 1. Call Limits

Every model call is bounded independently by:

* **Output token limit** — the maximum number of tokens the model may generate.
* **Execution timeout** — the maximum amount of time Kaiju will wait for the call to complete.

These limits solve different problems. Token limits constrain generation size; they do not constrain execution time.

For reasoning models, hidden reasoning tokens and visible output tokens are generated within the same overall output allowance. A model may therefore consume its entire output budget on reasoning and return no visible response.

Kaiju handles token allocation and execution time separately.

---

## 2. Output Token Budget

Each call receives an output token budget based on the type of operation being performed and the capabilities of the selected model.

### 2.1 Stage budget

Each stage defines a `budgetSpec` in `agent/budgets.go`.

A budget specification contains:

* a minimum token count;
* a fraction of the model's context window;
* a maximum token count.

For example:

`replyCodeBudget` uses:

`16,384 / 1⁄16 / 65,536`

Code generation receives a large allowance because truncating generated code generally produces an unusable result rather than simply a shorter one.

`replyEdgeBudget` is limited to 600 tokens because an edge primarily transfers information between stages rather than generating substantial new content.

### 2.2 Resolving the budget

`a.replyBudget(spec)` resolves the stage budget against the model's configured context window.

Conceptually:

`budget = clamp(model_window / share, floor, ceiling)`

If multiple window sizes are configured, the smallest is used.

If no model catalogue information is available, Kaiju uses the budget floor. This allows the budgeting mechanism to operate safely without requiring model metadata.

### 2.3 Model output limit

Before sending the request, `capReply` limits the calculated budget using the model catalogue's `max_output_tokens`.

The final value is never reduced below `budgetFloor`, currently 256 tokens.

The effective output budget is therefore approximately:

`min(stage_budget, model.max_output_tokens)`

subject to the configured minimum.

---

## 3. Reasoning Budget

When explicit reasoning budgets are supported, Kaiju divides the output allowance between reasoning and the expected final response.

`splitBudget` currently uses a 3:1 ratio:

| Allocation     | Example for an 8,192-token call |
| -------------- | ------------------------------: |
| Reasoning      |                           6,144 |
| Final response |                           2,048 |

The reasoning portion is sent as `reasoning.max_tokens`.

The overall output limit remains unchanged.

This prevents a reasoning model from consuming the entire generation allowance on hidden reasoning when the provider supports an explicit reasoning limit.

If either calculated portion would be below 256 tokens, Kaiju does not split the budget. Very small reasoning limits are unlikely to be useful and increase the risk of truncating reasoning without improving the final response.

### Provider support

Explicit reasoning budgets are not widely supported.

Testing found that only two of 56 evaluated model/provider combinations reliably honored the reasoning budget.

Model catalogue entries can therefore specify additional provider parameters through `parameters`. These values are passed through to the provider unchanged.

For OpenRouter requests, Kaiju can additionally request routing to a provider that supports the required parameters.

Because reasoning-budget enforcement cannot be assumed, Kaiju also implements recovery for cases where reasoning consumes the complete output budget.

---

## 4. Execution Timeout

Kaiju uses an execution timeout for model calls where the duration of the operation needs to be bounded.

The timeout is derived from the configured reasoning effort.

| Effort           | Timeout |
| ---------------- | ------: |
| `fast`           |    60 s |
| unset / `normal` |   120 s |
| `minimal`        |   120 s |
| `low`            |   120 s |
| `medium`         |   120 s |
| `high`           |   240 s |
| `xhigh`          |   480 s |
| `max`            |   960 s |

The default minimum is 120 seconds.

`fast` is the only configuration allowed below this minimum.

The 120-second baseline is based on observed model performance. For example, GPT-5 required a median of 77.3 seconds on the production planner prompt. A substantially shorter default timeout would terminate normal planner executions.

Provider reasoning effort and Kaiju's execution timeout are related but independent.

The configured effort value determines Kaiju's timeout. If the model catalogue indicates that the provider supports reasoning effort, the same value is also sent to the provider.

Kaiju does not infer reasoning effort from model behavior.

---

## 5. Model-Specific Timeout Multipliers

Some models consistently require more generation time. Their catalogue entries can specify a timeout multiplier.

The multiplier only increases the standard timeout.

| Configuration | Multiplier | Example      |
| ------------- | ---------: | ------------ |
| default       |         1× | normal model |
| `slow`        |       1.5× | `kimi-k2.6`  |
| `very slow`   |         2× | `qwen3-32b`  |

These values are based primarily on observed tail latency rather than median latency.

For example:

* `kimi-k2.6`: 5 of 26 live calls exceeded 115 seconds; p90 was 132.5 seconds.
* `qwen3-32b`: 386.1 seconds on the planner prompt, approximately five times the GPT-5 baseline.

The purpose of the multiplier is to avoid treating known model latency as a failed call.

---

## 6. Client Request Timeout

The model client also applies a request-level timeout to every call, including streamed calls.

| Request type                           | Timeout |
| -------------------------------------- | ------: |
| Standard request                       |   300 s |
| Reasoning requested or reasoning model |   600 s |
| Maximum connection timeout             | 1,200 s |

The standard timeout is 300 seconds because long planner generations have exceeded 180 seconds in production.

Calls involving reasoning receive 600 seconds because hidden reasoning is part of generation and can significantly increase total response time.

The absolute connection limit is 1,200 seconds.

This value is intentionally higher than any normal model-specific execution timeout so that it acts as a final network/request safety limit rather than terminating normal execution first.

### Timeout application by stage

The stage execution timeout is currently applied to:

* planner calls;
* chat calls;
* both compute calls.

Other stages rely only on the client request timeout:

* router;
* preflight;
* reflector;
* aggregator.

---

## 7. Reasoning Policy by Stage

Reasoning is controlled according to the type of operation being performed.

| Stage         | Reasoning behavior | Controlled by                       |
| ------------- | ------------------ | ----------------------------------- |
| Route         | Always disabled    | Engine                              |
| Light         | Always disabled    | Engine                              |
| Heavy planner | Enabled by default | Stage, with operator override       |
| Heavy compute | Model default      | Operator when explicitly configured |
| Answer / chat | Decided per turn   | Initial message-processing call     |

Routing and other small classification calls explicitly disable reasoning because hidden reasoning provides little value for short, constrained outputs.

The heavy planner enables reasoning by default because planning benefits from additional inference.

Heavy compute leaves reasoning at the model default unless the operator explicitly changes it.

For chat responses, the decision is made per turn by the call that first processes the incoming message and returns `think`.

### Failed reasoning decisions

If a stage fails to produce a reasoning decision — for example, because the router fails or the expected field is missing — Kaiju preserves the model's existing reasoning configuration.

Failure to make a decision is not interpreted as `thinking=false`.

This prevents an unrelated stage failure from silently changing the reasoning behavior of subsequent calls.

---

## 8. Call Completion and Recovery

A model call can terminate in four relevant states.

### 8.1 Normal completion

`finish_reason = stop`

The model returns visible content or a tool call.

No recovery is required.

### 8.2 Output limit reached with visible content

`finish_reason = length`

Visible content has already been generated.

The response was truncated because the output token limit was reached.

Kaiju does not automatically retry these responses because some partial outputs remain useful while others do not.

Recovery depends on the stage.

For the planner, completed steps generated before truncation can be retained.

For code generation, truncation is reported as an error rather than writing and executing incomplete code. Executing a partially generated program generally creates additional failures that obscure the original problem.

### 8.3 Output limit reached with no visible content

`finish_reason = length`

No visible content or tool call was produced.

This normally indicates that the model consumed the complete output allowance during reasoning.

This has occurred in production. One code-generation call ran for 239 seconds, consumed 14,276 tokens, and returned no code.

Kaiju handles this through `recoverDeadThought`.

The call is retried with reasoning disabled, and the reasoning generated by the original attempt is supplied to the retry.

The second call can therefore use the previous reasoning while spending its generation budget on the final response.

### 8.4 Execution timeout

The Kaiju execution timeout expires before the model completes.

Cancelling the request produces an error rather than a completed response. Consequently, there may be no final response object from which to retrieve the model's reasoning.

Calls subject to this timeout are therefore streamed.

Reasoning chunks are captured while generation is in progress.

If the timeout expires, Kaiju retries with reasoning disabled and supplies:

`cutThought(...)`

This contains the reasoning captured before cancellation.

The retry can continue from the work already performed instead of starting from an empty context.

---

## 9. Reasoning Capture

Kaiju can obtain reasoning from two sources.

### Completed response

For a successfully completed request, the response itself is authoritative.

Reasoning consists of:

1. the response's explicit reasoning field; and
2. reasoning extracted from `<think>...</think>` blocks in the final content.

Inline reasoning extraction occurs after the final stream chunk has been received.

### Stream capture

During streaming, Kaiju separately records:

* explicit reasoning chunks;
* inline thinking content.

This capture is required when a request is cancelled because a cancelled request may never produce a final response object.

For completed calls, the final response is preferred over the stream capture.

This ordering is important. A previous implementation preferred the stream capture, but inline `<think>` extraction occurs only after streaming completes. As a result, reasoning successfully extracted by the client could be discarded and absent from the resulting trace.

The current behavior therefore uses:

`completed response → stream capture fallback`

---

## 10. Model Catalogue Configuration

The model catalogue describes reasoning and timing behavior that differs between models or providers.

| Field               | Purpose                                                            | Source                           |
| ------------------- | ------------------------------------------------------------------ | -------------------------------- |
| `reasoning_efforts` | Determines whether reasoning effort is sent to the provider        | Measured provider/model behavior |
| `reasoning_budget`  | Determines whether an explicit reasoning token budget is requested | Measured support                 |
| `pace`              | Applies a model-specific timeout multiplier                        | Measured latency                 |
| `parameters`        | Sends additional provider-specific parameters                      | Manually configured              |
| `thinking`          | Declares reasoning capability and affects timeout/policy behavior  | Required catalogue metadata      |

`reasoning_efforts` is based on observed behavior rather than simply whether a provider accepts the parameter. Providers may accept an effort parameter without the selected model actually changing its behavior.

`reasoning_budget` is similarly based on observed enforcement. Only models demonstrated to honor the limit are configured to use it.

`pace` represents observed generation latency and increases applicable timeouts for unusually slow models.

`parameters` contains provider-specific values that Kaiju does not interpret. They are forwarded unchanged. If a parameter conflicts with a field controlled directly by the engine, the engine's value takes precedence.

`thinking` declares whether the model supports reasoning. Catalogue entries without this required information are rejected.

---

## Summary

Kaiju controls model execution through three independent mechanisms:

1. **Output budget** — limits total generated tokens.
2. **Reasoning budget** — limits how much of that output budget reasoning may consume when supported by the model.
3. **Timeouts** — limit how long Kaiju waits for generation.

Model-specific configuration adjusts these limits where observed behavior requires it.

When a reasoning model consumes its output budget or execution time without producing an answer, Kaiju preserves the reasoning already generated and retries with reasoning disabled. This avoids discarding completed reasoning while ensuring the retry spends its remaining generation capacity on the actual response.

---

## Implementation

| Area                                          | File                          |
| --------------------------------------------- | ----------------------------- |
| Stage budgets, `replyBudget`, `splitBudget`   | `agent/budgets.go`            |
| Effort to execution timeout                   | `agent/round_budget.go`       |
| Per-call setup, `applyReasoningBudget`        | `agent/ask.go`                |
| Timeout and recovery for compute calls        | `agent/heavy_round.go`        |
| Retry with reasoning disabled                 | `agent/recover_thought.go`    |
| Reasoning capture and source selection        | `agent/thinking_capture.go`   |
| Request timeout, multiplier, provider wire    | `agent/llm/client.go`         |
| Catalogue fields and lookups                  | `models/models.go`            |
| Per-turn reasoning decision                   | `agent/preflight.go`          |

Related: `docs/model-pace.md` (latency measurement and method), `docs/model-calls.md` (provider parameters), `docs/reasoning-effort-bench.md` (effort and budget measurement).
