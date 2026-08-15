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
        on every send             ask before sending
              │                        │
              │                        └── for a caller that must state the
              │                            budget in its prompt
              ▼
   Complete · CompleteStream · CompleteStreamResp
              ▲
              │
   ┌──────────┴────────┬────────────┬──────────┬──────────┐
  ask            askStream      web_fetch   uploads    memory
  askParsed      askStreamResp  ──────────────────────────────
   │                  │         hold a bare *llm.Client, taken
   └────── lane ──────┘         from ag.ExecutorClient(), so they
          + stamp                are sized without knowing it
```

## The lanes

Four, and the split is cost. A run makes one or two `Heavy` calls and a dozen
`Light` ones; sending the cheap work to the reasoning model multiplies the bill
for no gain, and sending the planner to the cheap one produces plans that do not
parse.

| lane | model | stages |
|---|---|---|
| `Heavy` | reasoning | planner, Holmes, microplanner, compute architect |
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

`ReplyCap(req)` answers what the cap will be without changing the request, for a
caller that has to state the budget in its prompt. Asking and sending give the
same answer: a number stated but not enforced is worse than saying nothing.

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
