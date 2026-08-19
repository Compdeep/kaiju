# Kaiju Framework — Design Notes

*Running record of decisions about kaiju's shape as something other projects build on.
Enbarr is the first, but the point is that it should not be the only one.*

*Started 2026-08-03, during the framework review.*

---

## Decided

### D1. Targets stay in kaiju; fleets do not

**`Target` is abstract enough to belong in kaiju.** A step naming the machine it runs
on is a general idea — drone fleets, data pipelines, robotic operators all need it.
Kaiju holds an opaque string and an interface; it never learns what a machine is, how
to reach one, or how one is chosen.

**`TargetValidator` stays in kaiju.** "Check this target string looks valid before
trying to reach it" is generic. The *implementation* — Enbarr's base58 peer-id check —
stays in Enbarr.

**`SetFleet` moves out.** `FleetContextProvider`, `FleetContext`, `PeerSource`,
`PeerSnapshot` are one product's vocabulary for one product's idea. Kaiju's own daemon
never sets one, so the feature exists solely for an embedder.

But the *capability* it provides is real: it injects "here are the machines you know
about" into three prompts, and without something like it the planner has nothing to put
in `Target`. So it is replaced, not deleted — by a neutral way for an application to
contribute text to a prompt. Enbarr then supplies its fleet listing through that, in
its own words, and kaiju never says "fleet" or "peer".

### D2. Services and per-agent settings are different things

Raised by the observation that some of this is process-wide and some is not. It
explains why kaiju has three package-level globals: they are services with nowhere to
live, so they became package variables — and that is why two agents in one process
cannot differ.

**Process-level (shared, set up once):**
prompt text · the LLM call observer · attribution headers · skill files read from disk ·
the embeddings client

**Per-agent:**
which model · permission level · event store · remote executor · target validator ·
DAG limits · data directory

Sketch:

```go
rt := kaiju.NewRuntime(kaiju.RuntimeConfig{ Prompts, Observer, Skills })
a  := rt.NewAgent(agentCfg)
```

Not yet built. It supersedes the three globals and is the proper fix for D6.

### D3. One agent per process, immutable, rebuilt on change

Settled earlier. Build once, pass it down by parameter, never mutate a live one. A
settings change builds a new agent and swaps the pointer atomically.

Requires `Close()`, which does not exist — see O2.

---

## Open

### O1. Fold the setters into settings

Eighteen `Set*`/`Init*` methods. Kaiju's own daemon makes eleven calls spread over 620
lines, and nothing tells a newcomer they exist, which are required, or in what order.

- **12 become settings fields** — anything the caller *hands over*: model details, the
  clearance checker, event store, remote executor, target validator
- **3 vanish into `New`** — `InitKernel`, `InitSkills`, `InitEmbeddings` are work, not
  input
- **3 stay as methods** — `SetToolEnabled`, `SetClearance`, `SetDAGEnabled` genuinely
  change while running, and should each say why in their documentation

Test to apply: *is this something I give the agent, or something the agent does?*

### O2. `Close()` does not exist

An agent starts five sets of goroutines — the engine loop, the skill-file watcher, the
scheduler dispatch loop and its workers — and nothing stops them.

Harmless while an agent is created once and lives for the process. Harmful the moment
O1 lands, because `New` would then start work, and rebuilding on a settings change
would leave the previous engine running: two loops pulling from one queue, two watchers
on the same files.

In Go this is not a memory leak — the collector cannot reclaim an agent whose goroutines
are live, so the old one keeps *working*. Worse than leaking memory.

`defer a.Close()` is the nearest thing Go has to a destructor. It is a convention, not
a guarantee: nothing enforces it and forgetting it is silent.

### O3. `Config` has 40 flat fields — **DONE** (`f7f3214`)

It was sixty by the time it was fixed, having gained six more on this branch.

Now seven embedded groups: `ModelConfig`, `PathConfig`, `IdentityConfig`, `DAGConfig`,
`RoutingConfig`, `ComputeConfig`, `Capabilities`.

**Embedded, not named.** Go promotes embedded fields, so `cfg.MaxNodes` and
`cfg.DataDir` still compile unchanged and none of the **235** read sites inside the
package were touched. A composite literal cannot use promoted fields, so construction
is the only thing that changes — which is the only place the grouping helps.

The trade, accepted deliberately: reads stay flat, so `cfg.MaxNodes` does not announce
that it is a DAG setting. Rewriting 235 call sites across fifteen files to gain that
would be a large mechanical change landing nowhere near the actual problem, which is
what an application must understand at construction. Field set verified identical
before and after.

Two dead settings found while measuring, **not fixed**:

- **`BootMDPath`** — declared, never read, never written. Dead.
- **`ComputeTimeout`** — written at `cmd/kaiju/main.go:344`, never read. A setting with
  no effect, the same class as the `Environment` bug fixed in `0ebf503`. Relevant to
  `compute.go`.

**Enbarr's own `Config` is not grouped.** It is still the fork and gets handled at
switch step 4. Measured: **36 of its 38 fields are shared with kaiju's**; the only
divergence is `Classifier` / `ClassifierDisabled` against kaiju's `ClassifierEnabled`,
an inverted flag. Kaiju has 24 fields Enbarr lacks, which arrive free.

### O4. "node" means two things

In kaiju a node is a step in a plan. In an embedding product it usually means a machine.
`Config.NodeID` and `Config.NodeRole` use the machine sense, and `NodeRole`'s values are
literally `"node"` or `"coordinator"` — so the word carries both meanings in one file.

Already caused one bug (`Trigger.TargetNode`, renamed to `Trigger.Target`).

**Rule going forward: `node` is a graph node. A machine is a `target`.**
`Config.NodeID` → `Config.ID`, `Config.NodeRole` → `Config.Role`.

### O5. Remaining vocabulary

`alertID` threads through internal signatures as the name for a correlation id. Not
public, but it is one product's word and reads oddly to anyone else. `RemoteRequest`
already calls the same thing `CorrelationID`.

### O6. Three package-level globals

`llm.SetCallObserver`, `llm.SetAttribution`, and `prompt`'s section variables. Each was
argued for individually; together they are a pattern nobody chose. Superseded by D2 if
that gets built.

---

## Thread safety — measured, not assumed

| | |
|---|---|
| One agent across many goroutines | **Safe.** Mutexes on the graph, job scheduler and agent. Race detector clean on `prompt`, `tools`, `llm`. |
| Many agents in one process | **Not isolated.** They share the package globals — a state-sharing problem, not a race. |
| `prompt.go` section variables | **Unguarded.** Safe only because they are written at boot and read thereafter. A latent race if anyone writes them later. |

The main `agent` package could not be race-tested — it exceeds the timeout, partly
because of a pre-existing hanging test (`TestScheduler_CapDropsWhenFull`, which also
hangs on Enbarr's master). So it is unverified rather than proven clean.

---

## Patterns worth keeping

**Standing convention as of 2026-08-04: new work follows these.** They are not
style preferences — each was arrived at by hitting the problem it solves. Where a
new extension point does not fit one of them, that is worth saying out loud
rather than inventing a sixteenth pattern.

The organising idea is **deep modules with shallow interfaces**: the surface an
application must understand should be far smaller than the behaviour behind it.
`RemoteExecutor` is one method over any transport that exists. `EventStore` is
two methods over any storage. That ratio is the thing to protect.

### How an application extends the engine

**1. Optional interface, type assertion, safe default.**
`Targeted`, `TypedExecutor`, `Outputter`, `Throttled`. A tool implements only
what it needs; a helper function does the assertion and supplies the default, so
no caller writes the assertion twice.

**2. The default must fail safe, not fail convenient.**
`RequiresTarget` defaults to **true**. Defaulting to false would read better in
the common case and would silently let a step omit its target and run on the
wrong machine. Choose the default by asking what happens when someone forgets,
not by what is tidier.

**3. Callback when the engine must ASK. Data field when only the application reads.**
This is the distinction that settled `Trigger`. `Cause any` is a plain field
because nothing in kaiju looks inside it. `DescribeTrigger` had to be a callback
because kaiju's own prompt code needs the answer and cannot get it from a field
it must not interpret. Ask which side needs to know: if the engine does, it is a
callback.

**4. Empty return means "not mine" — fall through to the built-in.**
`DescribeTrigger` returning `""` uses the default. An application handles only
the cases it recognises and leaves everything else alone, so adding a callback
never obliges anyone to reimplement what already worked.

**5. Nil capability means the feature is off, not broken.**
No executor and targets run locally. No validator and any target is accepted. No
store and nothing is recorded. A missing capability degrades; it never errors.

**6. Capabilities are configuration, not a sequence of calls.**
Everything arrives through `Config`, and an agent is fully formed when `New`
returns. The rejected alternative — `SetX` methods called after construction —
had already produced dead setters that nothing called and a targeting chain that
was never connected.

### How the boundary is drawn

**7. Opaque pass-through: carry it, hand it back, never read it.**
`Target`, `Cause`, `CorrelationID`, `TriggerType`, `Verdict`. Kaiju moves these
through and attaches no meaning. It is what stops the engine quietly learning
what a machine, an alert, or a conclusion is.

**8. Struct parameters at extension points, never positional.**
`RemoteRequest` is a struct so an executor can be given more context later
without breaking every implementation that already exists.

**9. Document what deliberately stays OUTSIDE.**
`remote.go` opens by listing the four things it refuses to know: what a target
name is, how to reach one, how one is chosen, what authority applies at the far
end. For an implementer that paragraph is worth more than the method signature,
because it says where their responsibility starts.

**10. Say what the caller cannot infer.**
`CallObserver` states that it runs inline on the calling goroutine, that the
response is nil on error, and that the key never appears in the request. None of
that is visible in the type.

**11. `Tool` is small and total** — five methods, nothing optional smuggled in.
Optional behaviour goes in a separate interface (pattern 1), never as a method
that implementations are expected to leave empty.

### Where this is not being followed

**`Config` has 40-odd flat fields** (O3) — model credentials beside file paths
beside execution limits. That is a wide interface on a deep module, which is the
opposite of the stated goal, and it grew by six more on this branch. Grouping it
is the outstanding correction; noted here so the convention is not read as a
claim that the code already satisfies it everywhere.

### Verification habit, not a code pattern

**12. Test the whole chain, not each link.**
`Trigger.Target` → `applyRunTarget` → `PlanStep.Target` → `Node.Target` →
`RemoteExecutor` was dead across four commits. Every link had a passing test. The
test that caught it walked end to end. When adding an extension point, write the
test that proves an application-supplied implementation is actually reached.
