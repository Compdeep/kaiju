# Model calls

Every call kaiju makes to a model goes through one door. This is what the door
does, why it exists, and what each layer owns.

## The two questions

A call to a model is never just a call. Two things have to be decided before the
request goes out, and they belong to different layers:

- **Which model answers** — the *lane*. The engine's business, because it
  depends on what the stage is for and on what the caller asked for.
- **How big the reply may be** — the *cap*. The client's business, because it
  depends on the model and on how much of its window the prompt already took.

Keeping those apart is why the door is thin.

## The picture

```
                    Config.Limits            the application's model catalog
                          │
                          │  given at construction, in the expression
                          │  that builds the client
                          ▼
                    llm.Client{ limits }
                          │
              ┌───────────┴────────────┐
              │                        │
        capReply(req)             ReplyCap(req)
        on every send             asked by the door, before sending
              │                        │
              │                        └── the number it then states in
              │                            the system message
              ▼
   Complete · CompleteStream · CompleteStreamResp
              ▲
              │
   ┌──────────┴────────┬────────────┬──────────┬──────────┐
  ask            askStream      web_fetch   uploads    memory
  askParsed      askStreamResp  ──────────────────────────────
   │                  │         hold a bare *llm.Client, taken
   └────── lane ──────┘         from ag.ExecutorClient(), so they
        + stamp                 are sized without knowing it
        + trace
           │
           └── writeTrace, on every send, from what the door already
               holds plus the TraceID the stage put on the context
```

## The lanes

Four, and the split is cost. A run makes one or two `Heavy` calls and a dozen
`Light` ones; sending the cheap work to the reasoning model multiplies the bill
for no gain, and sending the planner to the cheap one produces plans that do not
parse.

| lane | model | stages |
|---|---|---|
| `Heavy` | reasoning | planner, Holmes, microplanner, compute architect and coder |
| `Light` | executor | preflight, reflector, observer, context curator, plan validator |
| `Route` | pinned small model, else `Light` | the one decision made first: conversation or work |
| `Answer` | pinned answer model, else `Heavy` | the aggregator, and chat |

Each resolves a per-request override first — a trigger may name a provider and
model per lane — and falls back to the configured default.

## The door

```go
resp, err := a.ask(ctx, Heavy, req)        // send
resp, err := a.askParsed(ctx, Light, req)  // send, and report a cut reply
text, err := a.askStream(ctx, Answer, req, onChunk)
resp, err := a.askStreamResp(ctx, Answer, req, onChunk)
```

`prepare` holds what all four share: resolve the lane, stamp its model on the
request. The model has to be stamped first because the cap is looked up by model
id.

**`askParsed` is for callers that parse the reply.** A reply that stopped at the
token cap has no closing brace, and a caller that parses it reports malformed
input for a reply that was simply too big — then retries the same request. Nine
stages use it. A stage writing prose for a person uses `ask`: a cut answer there
is short, not unusable.

**The planner uses `ask`, deliberately.** It reads `finish_reason` itself and
retries asking for fewer, larger steps, which is better than reporting the
truncation. Wiring it to `askParsed` returns an error before that retry can run.

**Streaming has no truncation check.** `finish_reason` arrives in the final
frame, and a streaming stage has already shown the text to a person by then.

## The cap

`llm.Client.Limits(fn)` gives a client the application's catalog. Without it,
every request goes exactly as its caller wrote it — an application that supplies
no catalog is unaffected.

With it, `capReply` runs on every send and lowers `MaxTokens` to the smaller of:

- the model's published maximum reply, and
- `context window − prompt − headroom`, the prompt estimated at four characters
  per token

It never raises what the caller asked for, and never settles below `replyFloor`
(256) — a prompt that nearly fills the window would otherwise compute a cap of a
few tokens, which fails in a way that looks like the model refusing to answer.

`ReplyCap(req)` answers what the cap will be without changing the request.
Asking and sending give the same answer: a number stated but not enforced is
worse than saying nothing.

## The stated budget

`max_tokens` is not a hint. The model is never shown the number; the provider
counts tokens as they are generated and stops at it, mid-sentence and
mid-object. A model writing to its own sense of length is cut wherever that
lands, and every stage that parses the reply then reports malformed input for an
answer that was simply too long.

The only channel to the model is the prompt, and the only moment the number is
final is after the lane is resolved and the cap settled. So the door fixes the
cap and appends one line to the system message:

> Reply budget: about 2048 tokens. Generation stops there, so a longer reply is
> cut off part-way and cannot be used. Plan the length before you start.

Four rules, each with a reason:

- **The number stated is the number sent.** The door sets `req.MaxTokens` to
  `ReplyCap` before stating it, so `capReply` finds nothing left to lower.
- **Once, however often the request is sent.** The planner builds its retry from
  the same message slice as its first attempt, so the line is recognised by its
  opening words rather than appended again.
- **Not below 256 tokens.** A forced `route()` call takes 16; the sentence would
  be larger than the budget it describes.
- **The first system message only, and nothing without one.** A request with no
  system message is a caller talking to the model directly, and this package
  does not edit that.

## What the provider says went wrong

Two failures used to arrive as silence, and silence is read by every caller as
an answer it could not use.

**A 200 that carries an error.** An OpenAI-compatible gateway answers an
upstream failure, a rate limit or a filtered request with HTTP 200, an empty
`choices` list, zero usage, and an `error` object as the only account of it.
That object was not decoded, so a caller saw a reply with no choices and no
reason, and the trace recorded an empty response with nothing against it —
6.9% of one deployment's planning calls, each abandoning a run.
`ChatResponse.Error` is decoded now and returned as the error it is, so the
caller and the trace both say what the provider said. A 200 with no choices and
no error object carries a bounded slice of the body instead, because the body is
then the only evidence and it is unrecoverable after the fact.

**A provider that has stopped answering.** Every one of those calls is billed
for what the model generated before it was abandoned, returns nothing, and the
caller's retry sends the same work again: 46 of 57 calls in three hours on one
deployment, 1.15M tokens of which roughly 40% was paid for and thrown away. Ten
consecutive upstream failures open a breaker (`agent/llm/breaker.go`), after
which requests fail fast with `ErrProviderUnavailable` — which a caller can tell
from a failure of its own, because nothing was sent, so nothing was billed and
nothing about its prompt is implicated. Five minutes later exactly one request
is allowed through and its outcome decides, so a provider still down costs one
call per cooldown rather than one per caller.

Only the provider's own failures count toward it. A truncated reply, an answer
that will not parse, a model that ignored the schema — those are answers, and a
run of bad ones must not stop every caller from asking.

## The trace

The door writes one `LLMTrace` per send. It already holds seven of the fields —
the run, the model, the start time, the latency, the prompts, the reply, the
token counts — so a stage supplies only what is its own:

```go
ctx = withTrace(ctx, TraceID{NodeID: id, NodeType: "observer", Tag: tag,
	Input: map[string]string{"node": completedNode.Tag}})
resp, err := a.completeLight(ctx, req)
```

A call made without `withTrace` is still sent and simply not traced, which is
what a call outside any stage should do.

**`traceFault(ctx, why)` is for what the door cannot know.** The door writes
when the call returns, so it has no view of what the stage then made of the
reply — a forced tool call that carried no arguments, or arguments that would
not parse. It writes a short second entry naming the same node, landing under
the call it is about. The log is a file, appended to and never rewritten, so
amending the first entry is not open to us.

**`retracing(ctx, tag)` is for a stage that calls the model more than once.**
The planner makes four calls — the plan, then asking for a shorter one, one
that parses, and one that names real tools — and entries that read the same are
entries nobody can tell apart. Each retry is re-tagged with why it ran.

## Reasoning

Thinking is not a mode, it is generation. Its tokens come out of the same
completion allowance as the answer and its time out of the same clock, so a call
that thinks is a call whose size and deadline are different.

Three questions travel together and are easy to confuse — whether to think, how
hard, and how much of the reply it may use. A model may answer one and ignore
the others, so each is decided separately and each is asked for only where the
catalog says this model acts on it.

### What a caller says

`ChatRequest.Think` carries the intent, in terms that do not depend on the
provider:

```go
req.Think = &llm.Reasoning{Want: llm.WantOn, Effort: llm.EffortHigh}
```

`WantAuto` is the zero value and means **say nothing**, which is not `WantOff`.
Reading silence as off lets a caller that never considered the question change
how every answer it touches is written.

`ChatRequest.Reasoning` is the OpenAI-shaped parameter that goes out. It is set
by the client and never by a caller: what travels depends on what the catalog
says the model acts on, and a caller does not know that.

### What decides

Four steps, in `agent/llm/resolve.go`, and the order is the design.

1. **A request that forces ONE shape does not think unless it says so.** Its
   reply is bounded by a schema and its thinking is not: over 257 calls on a
   trivial prompt, reasoning ran to a median of 139 tokens and a maximum of
   16,002 — see [reasoning-effort-bench.md](reasoning-effort-bench.md). No
   comparison against the reply cap survives that tail, so this is decided by
   what the call *is*. The planner forces one shape and says `WantOn`.
2. **Then what the caller asked.**
3. **Then what the model can do.** An `Off` to a model whose reasoning is
   mandatory is not sent — it reads as success and changes nothing. An effort
   outside the measured set is not sent. A budget to a model that does not
   honour one is not sent. A model the catalog cannot answer for is sent
   nothing at all.
4. **Then the clock.** The ordinary deadline, doubled when the reply will carry
   reasoning, multiplied by the model's measured pace.

The **effort ladder does not scale this deadline**. It answers "has the provider
stopped answering", which is the same question at any effort — and widening it
would loosen the connection ceiling, which is the only bound a streamed call
has. How long a piece of *work* may take is the round deadline, above this
package.

### Who chooses

Above the client, `agent.thinkingFor` states the precedence once: the lane's own
rule, then the run's choice, then the operator's, then nothing.

| level | where | wins over |
|---|---|---|
| the lane | `Light` and `Route` refuse thinking | everything |
| the run | `Trigger.ReasoningEffort`, from `reasoning_effort` on the request | the node |
| the node | `llm.reasoning` and `llm.reasoning_effort` in the config | nothing |

`Light` and `Route` are wider than the client's rule on purpose: not every call
on those lanes forces a shape, and a 256-token repair suggestion cannot afford
to think either.

### What an embedding application has to supply

One lookup. Without it every request goes exactly as its caller wrote it — the
same contract `Limits` has always had, and what the tests in
`agent/llm/inert_test.go` hold to.

```go
client.Catalog(func(model string) (llm.ModelFacts, bool) { … })
```

`ModelFacts.Thinking` is five measurements, not capabilities a provider
advertises: whether it reasons by default, whether that can be switched off,
which efforts it acts on, whether it honours a token budget, and how much longer
it takes than the baseline. Every provider accepts every reasoning parameter and
none errors on any of them, so a model that ignores one answers exactly like a
model that acts on it — asking the provider tells you nothing. kaiju's own
implementation is `configapi.Facts`.

### What comes back

`Message.Reasoning` is the one place a caller reads the thinking, whichever of
the three ways the model delivered it: a reasoning field, reasoning chunks on a
stream, or `<think>` written into the answer, which is lifted out on both paths.

`Usage.ReasoningTokens()` is what it cost. Reasoning is billed inside
`CompletionTokens`, so without the breakdown it cannot be told apart from the
answer. A provider that does not send one reports nothing rather than zero.

### Seeing it

`LLMTrace.Asked` and `LLMTrace.Sent` are the same instruction as the stage meant
it and as it reached the wire. They differ exactly when the catalog narrowed
something, which is the only way a reader can tell "the setting did nothing"
from "the setting did nothing and here is why".

## What is not here

**The prompt's own size.** Trimming evidence to fit a budget happens before the
door, in the ContextGate — see [prompt-context.md](prompt-context.md). Only the
caller knows which source matters, so only the caller can choose what to drop.

**The planner's budget.** `planMaxTokens` is the one number the engine computes
rather than picks: the planner is told it may write up to `MaxNodes` steps, so
its cap has to fit that many. It lives in `agent/reply_cap.go`.

**Embeddings.** `Embed` has no lane and needs none — no reply to size, no
budget to state, no truncation to check.

## Why one door

Each of these was applied at the call site, so each was applied differently or
not at all. Before this, nine of eighteen calls sized their reply against the
model and nine did not — including the aggregator, which asks for the largest
reply the engine ever makes. A step added later had to be added everywhere: the
truncation check cost three functions because there were three doors.

Four callers could not have the cap at all, because it needed the catalog and
the catalog lived on `agent.Config`: `web_fetch`'s two summarisers, upload
extraction, and memory compaction each hold a bare `*llm.Client` and no
`*Agent`. Moving the cap onto the client gave it to them without their changing
a line — they take their client from `ag.ExecutorClient()`.
