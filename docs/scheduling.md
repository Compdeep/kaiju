# Scheduling

Every kaiju run has two independent kinds of concurrency, and it helps to keep them apart:

- **Cross-request** — many investigations, from many callers, sharing one process. A single **priority worker pool** decides which run executes now, which waits, and which is dropped. This is `job_scheduler.go`.
- **Intra-request** — one investigation running its own DAG. Inside a single run the scheduler fires every ready node at once as **node batches**. This is the batch loop in `scheduler.go` (see [graph.md](graph.md) for the node/reflector machinery).

This doc covers the run *lifecycle* around those two layers: how a request is enqueued, how a newer message preempts an older one, how a Stop actually tears a run down, and how an interjection steers a run without stopping it.

## Overview

```
   HTTP request                 ┌───────────────────────────────────────────┐
   POST /api/v1/execute         │  Priority worker pool  (job_scheduler.go)   │
        │                       │                                             │
        ▼                       │   jobHeap (min-heap by priority, then seq)  │
   Agent.SubmitSync             │     PriorityChat(0)  ─┐                      │
        │                       │     PriorityBackground(10)                  │
        ▼                       │                       ▼                      │
   Kernel.SubmitSync ──────────▶│   worker · worker · worker   (max_concurrent)│
   schedulePolicy               │        │                                    │
   (chat_query → Chat,          │        │  one worker reserved for chat       │
    else Background)            │        ▼                                    │
                                │   s.exec(job.ctx, job.trigger)              │
                                └────────┬────────────────────────────────────┘
                                         ▼
                              Agent.RunDAGSync  (one full DAG per job)
                                         │
                                         ▼
                              ┌──────────────────────────┐
                              │  node batches           │  launchReady() fires every
                              │  (scheduler.go batch loop) │  node whose deps are terminal
                              └──────────────────────────┘  as concurrent goroutines
```

The kernel owns exactly one `Scheduler`; the scheduler's executor callback is `Agent.RunDAGSync`, so **one popped job is one full DAG run**. The scheduler replaced the old single-flight design (a lone `activeInv` plus a global `investigating` flag) — the pool is now the only front door for work.

## The priority worker pool

`internal/agent/job_scheduler.go`. One priority queue feeding a fixed pool of worker goroutines.

### Priorities

Every job carries a `Priority`, and the queue is a min-heap ordered by `(priority, seq)` — lower priority value runs first, FIFO within a class:

```go
const (
    PriorityChat       Priority = 0  // interactive, operator-facing
    PriorityBackground Priority = 10 // fire-and-forget / non-interactive triggers
)
```

`schedulePolicy` (`kernel.go`) maps a trigger to its priority and session key: a `chat_query` trigger is `PriorityChat` keyed by `SessionID`; everything else is `PriorityBackground`. The kernel stays ignorant of what a session *means* — `SessionID` is an opaque conversation identifier the host assigns.

### Workers and the reserved chat lane

The pool size is `max_concurrent` (config `agent.max_concurrent` → `MaxConcurrentInvestigations`), defaulting to **3** when unset or invalid (`defaultConcurrency`). Because each run does its own per-graph work and jobs are keyed by session, running several at once is safe.

One worker is always kept free for an interactive caller. The scheduler tracks `maxBackground = workers − 1` (floored at 1): when the job at the top of the heap is `PriorityBackground` and `runningBg` has already reached `maxBackground`, `pickLocked` holds it back rather than filling the last worker with background work — so a chat query arriving under a flood of background triggers still finds a lane. At `workers == 1` there is no reservation possible (`maxBackground` floors to 1) and the lone worker must still run background jobs. During shutdown the reserve is dropped so every remaining job drains.

### The queue depth cap

`maxQueueDepth = 100` bounds the *pending* queue. When it is full, a new `PriorityBackground` job is dropped (logged, `enqueue` returns false → `ErrQueueFull` to a sync caller). **Chat is exempt**: an interactive query is operator-driven, session-deduped, outranks background work, and already has a worker reserved for it, so it flows through even when the background queue is at cap rather than being rejected.

### Live resize

`SetConcurrency(n)` retunes the pool at runtime. Growing spawns workers immediately; shrinking retires idle or just-finished workers down to `n` (a job already in flight is never interrupted — a shrunk worker checks `liveWorkers > workers` only between jobs). It also retunes the reserved chat lane (`maxBackground = n − 1`). `n < 1` clamps to 1.

### The Job

```go
type Job struct {
    trigger   Trigger
    priority  Priority
    session   string
    ctx       context.Context      // parent of the run's dagCtx; cancelling it unwinds the DAG
    cancel    context.CancelFunc
    resultCh  chan invResult        // non-nil for synchronous callers (chat)
    interject chan string           // per-query steering channel, buffered (interjectBuffer = 8)
    preempted atomic.Bool
    // ... seq, heap index, heartbeat fields
}
```

Two entry points put a job on the heap:

- **`SubmitSync(ctx, trigger, priority, session)`** — the chat path. It makes a buffered `resultCh`, enqueues, and then **blocks on `resultCh`** until the run finishes, is preempted (`ErrPreempted`), or the caller's `ctx` is cancelled. The worker delivers the `SyncResult` on that channel.
- **`Submit(trigger, priority, session)`** — fire-and-forget (background triggers). No `resultCh`; the result is delivered nowhere.

A worker loops: `pickLocked` pops the highest-priority eligible job, the worker records it in `running[session]`, then calls the executor as `s.exec(withInterject(job.ctx, job.interject), job.trigger)`. That single call is `RunDAGSync` — the job's cancel context and its steering channel both reach the DAG through the context.

## Preemption — newest message wins

A conversation is a single session, so a newer message in a session should never race the older one. `enqueue` handles the still-queued case directly:

- If a job for this session is **still queued** (not yet picked up by a worker), the new job **supersedes** it: the old job is removed from the heap, marked `preempted`, its context cancelled, and its sync caller (if any) receives `ErrPreempted`. For chat this is *newest wins*; for background it *dedupes a repeat trigger*.

- If the session's query is **already running**, it never reaches `enqueue` as a competing job. The chat front door routes a message for a running session into that run as an **interjection** instead (see below) — `Scheduler.Interject` returns `true`, and the caller does not start a second query. A running query is never abandoned or duplicated by a follow-up message.

So `PriorityChat` newest-wins operates on the *queue*; a live run is *steered*, not replaced.

## Node batches within one run

Inside `RunDAGSync`, the scheduler walks the graph in dependency order but does **not** serialize node-by-node. The `launchReady` closure fires **every** node whose dependencies are all terminal, each as its own goroutine:

```go
launchReady := func() {
    for _, n := range graph.ReadyNodes() {   // deps all resolved/failed/skipped
        graph.SetState(n.ID, StateRunning)
        inflight++
        go a.fireNode(ctx, n, graph, budget, completionCh, ...)   // (or fireReflection / fireHolmes by type)
    }
}
```

That fan-out is the parallelism spine — two searches with no dependency between them run at the same time. Bounds on the fan-out:

- **Per-tool throttle** — `toolThrottle` imposes a per-tool cooldown so a batch can't hammer one tool.
- **Budget** (`dag.go`) — `Budget.TrySpawnNode(skill, isLLM)` is an atomic check-and-set enforcing `MaxNodes`, `MaxLLMCalls`, and a per-batch `MaxPerSkill` cap. Infra LLM calls (preflight, executive, reflection, aggregator) charge only the LLM budget.
- **No hard wall clock** — a run has no deadline timer. The only time-based nudge is the kernel's **heartbeat**, a soft stuck-detector (see Interject below).

The batch loop's `select` waits on `ctx.Done()` and a `completionCh`; each completed node may graft children, and each iteration ends by injecting any pending interjection and calling `launchReady` again. The full node-type handling (tool success/failure, reflection, Holmes, observer) is documented in [graph.md](graph.md).

## Stop / cancel — the real teardown

A **Stop** is the only thing that actually tears a running DAG down. The path is a straight line:

```
POST /api/v1/stop  (handleStop, session_id)
      │
      ▼
Agent.Cancel(session)
      │
      ▼
Kernel.Cancel(session)
      │
      ▼
Scheduler.Cancel(session)  →  job.cancel()
```

`Scheduler.Cancel` looks up `running[session]` and calls the job's `cancel()`, which cancels **`job.ctx`**. That context is the parent of the run's `dagCtx` (`dagCtx, cancel := context.WithCancel(ctx)` in `RunDAGSync`), so cancelling it propagates into the whole run:

- The batch loop's `select` hits its `<-ctx.Done()` case, drops the inflight count, and stops launching new batches — in-flight nodes are abandoned.
- Before the aggregator runs, `RunDAGSync` checks `dagCtx.Err()`; if the context is cancelled it returns early, so **the aggregator is skipped** and no final synthesis is produced.

`Scheduler.Cancel` returns `true` if a query was running. It uses the same `job.cancel()` that preemption uses when a newer same-session message supersedes a queued job.

**Contrast with a client disconnect.** If the HTTP caller just drops the connection, only the `SubmitSync` *wait* ends (its `ctx.Done()` fires and it stops blocking on `resultCh`) — **the worker keeps running the job to completion**. Cancelling the connection does not cancel the job context. Stop is what actually unwinds the DAG; a disconnect merely stops someone waiting for the answer.

## Interject — soft steering mid-run

An interjection steers a run **without** stopping it. The path mirrors Stop up to the scheduler:

```
POST /api/v1/interject  (handleInterject, session_id + message)
      │  → Agent.Interject → Kernel.Interject → Scheduler.Interject(session, msg)
      ▼
running[session].interject <- msg     // buffered channel, cap interjectBuffer = 8
```

`Scheduler.Interject` pushes the message onto the running job's `interject` channel and returns `true`. If no query is running it returns `false`, and the caller starts a normal query instead. If the buffer is full the steer is dropped and logged (no human types faster than a run reads, so the buffer is a safety valve, not a queue).

The message reaches the DAG through `withInterject` / `interjectFrom` on the context. Each batch-loop iteration calls `injectInterjection`, which does a **non-blocking** read of the channel. When a message is present it:

1. Adds a `NodeInterjection` node to the graph.
2. Calls `graph.GatePending` so all currently-pending nodes wait behind it.
3. Fires `fireInterjectionReflection` — the operator's message becomes the **focus of the next reflection**, which then steers the run (continue / replan / conclude) with the human's guidance folded in.

So an interjection is soft: it doesn't cancel anything; it becomes the next reflection's context and lets the reflector re-aim the plan.

**The heartbeat auto-interjects.** The kernel's heartbeat module (`kernel.go`, `checkJobProgress`) reads the scheduler's `RunningJobs()` and counts recent failures in each run's worklog. After a bounded number of consecutive stuck ticks it injects a progress-check message through the same `Interject` path — nudging a run that keeps failing the same fix toward Holmes or an honest conclusion, rather than letting it spin. This is the closest thing to a timeout; there is no hard wall clock.

## Relevant source

| file | responsibility |
|---|---|
| `internal/agent/job_scheduler.go` | priority queue + worker pool, `Job`, preemption, reserve, queue cap, `SetConcurrency` |
| `internal/agent/kernel.go` | `schedulePolicy`, `Submit`/`SubmitSync`/`Interject`/`Cancel` front door, heartbeat auto-interject |
| `internal/agent/scheduler.go` | `RunDAGSync`, `dagCtx`, the batch loop, `launchReady`, `injectInterjection`, aggregator skip on cancel |
| `internal/agent/dag.go` | `Budget` (`MaxNodes`/`MaxLLMCalls`/`MaxPerSkill`), `ReadyNodes`, `GatePending` |
| `internal/api/api.go` | `handleStop` (`POST /api/v1/stop`), `handleInterject` (`POST /api/v1/interject`) |
| `internal/config/config.go` | `agent.max_concurrent` → worker-pool size |
