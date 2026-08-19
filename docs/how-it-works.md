# How the Kaiju Integration Works

> **Kaiju must stay domain-independent.** No Enbarr concepts, schemas, security
> rules or business logic in it — no queen, pawn, alert or triage, in code,
> comment or test fixture. Enforced by `agent/no_domain_leak_test.go`.
>
> A generic improvement may be contributed back. Where Enbarr's implementation
> is demonstrably better — simpler, more robust, more capable — and carries no
> domain meaning, generalise it into Kaiju rather than discarding it. This has
> been done for the shipped-skills directory, the clearance on `UpdateGate`,
> the unknown-field warning on plan parsing, `PathConfig.BuiltinSkills`,
> `SearchConfig.HTTPClient` and twenty-five tests.

*Mechanism, not analysis. What the two repositories look like, what the seam actually
is as code, what happens when you type `go build`, and how Kaiju still stands on its
own afterwards.*

---

## 1. The two repositories, before and after

### Before — today

```
/home/sites/kaiju/kaiju/                     module github.com/Compdeep/kaiju
├── internal/agent/          22,878 lines    ← the engine. NOBODY outside kaiju can import this.
├── internal/tools/           5,957 lines    ← kaiju's own tools (bash, file, git, web)
├── internal/{api,channels,gateway,web}      ← kaiju's daemon and UI
└── cmd/kaiju                                ← kaiju's binary

/home/sites/omamori/OmamoriNet/              module github.com/omamori/omamori-net
├── internal/agent/          18,623 lines    ← a COPY of kaiju's engine, drifted ~40%
│   └── builtin/              6,492 lines    ← Enbarr's 24 security tools
├── internal/streams/                        ← libp2p
├── internal/ipc/                            ← C++ named pipe / unix socket
└── cmd/omamori-net                          ← Enbarr's binary
```

Two copies of one engine. No import relationship. Changes move by hand.

### After

```
/home/sites/kaiju/kaiju/                     module github.com/Compdeep/kaiju
├── pkg/agent/               22,878 lines    ← SAME CODE, moved out of internal/. Now importable.
├── internal/tools/           5,957 lines    ← unchanged, still private to kaiju
├── internal/{api,channels,gateway,web}      ← unchanged, still private to kaiju
└── cmd/kaiju                                ← imports pkg/agent

/home/sites/omamori/OmamoriNet/              module github.com/omamori/omamori-net
├── go.mod                   require github.com/Compdeep/kaiju v0.4.0
├── internal/agent/          ~3,000 lines    ← wrapper + Enbarr-only files ONLY
│   └── builtin/              6,492 lines    ← unchanged
├── internal/streams/                        ← unchanged
├── internal/ipc/                            ← unchanged
└── cmd/omamori-net                          ← imports kaiju/pkg/agent
```

One engine. Enbarr's 18,623-line copy is deleted. **The only file that moved in Kaiju
is `internal/agent/` → `pkg/agent/`.** Nothing was added to Kaiju except ~40 lines of
interface.

---

## 2. The seam is one function

The seam is not a layer, a folder, or a protocol. **It is `createAgent()` in
`cmd/omamori-net/main.go`** — the function that already exists today at line 3070.
Everything Enbarr gives Kaiju passes through it, and there are exactly five kinds of
thing.

```go
package main

import (
	kagent "github.com/Compdeep/kaiju/pkg/agent"
	"github.com/Compdeep/kaiju/pkg/agent/prompt"

	"github.com/omamori/omamori-net/internal/agent/builtin"  // Enbarr's tools
	"github.com/omamori/omamori-net/internal/streams"        // Enbarr's libp2p
)

//go:embed prompts.md
var enbarrPrompts []byte

// ── (1) PROMPTS ─ ONCE AT BOOT, never from createAgent.
//
// prompt.Soul / prompt.Aggregator / … are package-level variables. Apply and
// Load WRITE them. createAgent is called again at main.go:1507 from the
// dashboard settings handler, while investigations are in flight and reading
// those variables — so calling Load there is a data race. Prompts are
// write-once at boot, read-only thereafter.
func RunNode(ctx context.Context, cfg *config.Config, ...) error {
	if err := prompt.Apply("enbarr/prompts.md", enbarrPrompts); err != nil {
		return err                                 // fail-closed
	}
	if err := prompt.Load(cfg.DataDir); err != nil {
		return err                                 // operator override, still fail-closed
	}
	// ... agentInstance = createAgent(...)
}

func createAgent(cfg *config.Config, hub *gossip.Hub, bridge *ipc.Bridge, ...) *agent.Agent {

	// ── (2) CONFIG ─ plain values.
	engine, _ := kagent.New(kagent.Config{
		LLMEndpoint: cfg.AgentLLMEndpoint,
		DAGMode:     cfg.AgentDAGMode,
		NodeRole:    string(cfg.Role),
		// ...
	}, hub, bridge, nodeID)

	// ── (3) TOOLS ─ Enbarr registers its own. Kaiju's engine never imports them;
	//                it looks them up by name in the registry at dispatch time.
	reg := engine.Registry()
	reg.Register(builtin.NewGetTelemetry(cfg.DataDir))
	reg.Register(builtin.NewPortScan())
	reg.Register(builtin.NewKillProcess())
	// ...21 more

	// ── (4) INTERFACES ─ Kaiju declares them; Enbarr implements them.
	//                     This is how the engine reaches libp2p and the C++ service
	//                     without knowing either exists.
	engine.SetRemoteExec(streams.NewToolExecClient(h))     // libp2p lives in here
	engine.SetTargetValidator(validatePeerIDFormat)
	engine.SetEventStore(shim)                             // sqlite lives in here
	engine.SetFleet(&fleetContextAdapter{...})

	// ── (5) WRAPPER ─ Enbarr's own additions, on Enbarr's own type.
	//                  Kaiju has no knowledge of these.
	a := &agent.Agent{Agent: engine}
	a.SetAlertGate(investigationGate.Allow)
	a.SetClassifier(triageClassifier)
	a.RegisterPreflightPlugin(agent.NewFleetTargetPlugin())
	return a
}
```

That function *is* the integration. There is nothing else — no symlink into Kaiju, no
build tag, no plugin loading, no configuration file describing the binding.

### DECIDED: Enbarr instantiates a Kaiju singleton — constructor-only, immutable

**Enbarr constructs exactly one Kaiju engine instance, at the top of `RunNode`, and
passes it down by parameter. The instance is immutable after construction. A
configuration change builds a new instance and swaps the pointer atomically; it never
mutates a live one.**

This is a deliberate choice, stated explicitly so it is not re-litigated:

| Property | Consequence |
|---|---|
| One instance per process | one tool registry, one IGX gate, one clearance value — a single audit point, no second agent running under different policy |
| Constructed at top level, passed down | every consumer's dependency is visible in its signature; a test constructs its own; Kaiju's own daemon constructs a second, independent one |
| **Immutable after `New`** | no runtime path to inject a tool or raise clearance after boot — see `SetClearance` below |
| Config change = rebuild + atomic swap | removes the data race by construction rather than adding a mutex |

**The security property comes from immutability, not from single-instance.** A
singleton does not gate access — Go has no access control on a pointer, and anything
holding `*Agent` has every exported method. Access is gated by the IGX Triad Gate in
the dispatcher, which works the same with one instance or a hundred. What immutability
buys is that the registry and clearance cannot change after boot.

The concrete case: `main.go:1575` currently calls `SetClearance` on the live agent from
the promotion-beacon handler, raising the node's IGX clearance in place while the
dispatcher may be reading it. Under constructor-only, promotion rebuilds at the new
clearance and swaps — one observable event, no torn state.

**A mutable shared instance is a risk to Go's memory safety, not a support for it.** Go
is memory-safe regardless of instance count; the one thing that can break its
guarantees is a data race on shared mutable state, where concurrent writes to an
interface value or map can produce torn values. That is why immutability is load-bearing
here and not a stylistic preference.

#### What this changes

Twelve setter call sites exist today. Nine are construction-time and collapse into
`Config` fields directly:

```
1357 SetFleet   1363 SetCapability   1364 RegisterPreflightPlugin   1411 SetConvoStore
1420 SetRemoteExec   3188 SetEventStore   3195 LoadIntentRegistry
3224 SetExecutorClient   3311 SetAlertGate
```

Three mutate a live instance and become rebuild-and-swap:

```
1483 SetLLMClient       ← dashboard settings hot-swap
1500 SetExecutorClient  ← dashboard settings hot-swap
1575 SetClearance       ← promotion beacon; security-relevant
```

The rebuild path already exists at `main.go:1507` as the fallback when a hot-swap is
not possible. Under this decision it becomes the only path.

```go
// The holder — the one piece of shared mutable state, and it is a single pointer.
var agentRef atomic.Pointer[agent.Agent]

// Boot, and every config change thereafter:
agentRef.Store(buildAgent(cfg))

// Every consumer:
a := agentRef.Load()          // race-free; an in-flight investigation keeps its instance
```

An investigation already running holds its own pointer and finishes against the
instance it started with. Nothing is mutated underneath it.

#### REQUIRED: the engine needs a shutdown path — it has none today

Rebuild-and-swap only works if the replaced instance can be stopped. It currently
cannot. `*Agent` has **no `Close`, `Stop`, `Shutdown` or `Cancel` method**, and it owns
four long-lived goroutine sets, all bound to the process-lifetime context passed at
`main.go:3314` (`go agentInstance.Start(ctx)`):

| Goroutine | Started at |
|---|---|
| kernel event loop | `agent.go:491` — `go a.kernel.Run(ctx)` |
| job-scheduler workers | `job_scheduler.go:164`, `:231` — `go s.worker()` |
| skill hot-reload watcher | `agent.go:806` — `go w.Start(ctx)` |
| scheduler dispatch loop | `job_scheduler.go:152` |

Swapping the pointer without stopping the old instance leaves those running. The
consequence is worse than a leak: **two kernels would drain the same job queue
concurrently, and two skill watchers would write the same registry.**

**This is already a latent bug, not one the decision introduces.** `main.go:1507`
rebuilds the agent today on a dashboard settings change, and the previous instance's
goroutines are never stopped. The decision makes rebuild the routine path, which turns
an occasional leak into a guaranteed one — so the fix becomes mandatory rather than
advisable.

Required addition to Kaiju's public API:

```go
// New derives a per-instance context; Close cancels it and waits for the
// kernel, scheduler workers and skill watcher to exit.
func (a *Agent) Close() error

// Swap becomes:
old := agentRef.Swap(buildAgent(cfg))
if old != nil {
    _ = old.Close()          // after in-flight work drains, or with a deadline
}
```

Open question for the design: whether `Close` waits for in-flight investigations or
cancels them. Waiting is safer and matches the incident queue being sequential;
cancelling risks a half-finished investigation with no verdict recorded. Recommend
waiting, with a timeout that logs rather than forces.

#### Prompts follow the same rule

Given constructor-only, prompt sections should move from package-level variables onto
`Config` as a value. That removes the last piece of global mutable state in the design,
makes prompts per-instance, and lets a rebuild change prompts safely. See the note
below for what the package-variable form would otherwise force.

### The prompt package as it stands today

`agentInstance` is a local variable in `RunNode`, built by an ordinary constructor and
passed explicitly to everything that needs it (the incident queue, the fleet sweep, the
dashboard, the IPC loop). There is no `GetInstance()`, no `once.Do`, no package-level
instance. That is ordinary dependency injection, and it is what makes the engine
embeddable: Kaiju's own daemon constructs a second, independent instance from the same
constructor.

**The prompt package is different, and is the one piece of genuine global mutable state
in this design.** `prompt.Soul`, `prompt.Aggregator` and the other 17 section variables
are package-level. `Apply` and `Load` write them; every LLM call reads them. Two
consequences to respect:

1. **Load at boot only.** `createAgent` is called again at `main.go:1507` from the
   dashboard settings handler, while investigations are running. Calling `Load` there
   would write those variables under concurrent readers. Prompts must be write-once at
   startup, read-only afterwards — the shape the package was designed for ("Runs once
   at boot; performs no per-query IO").
2. **Prompts cannot vary per instance.** Two agent instances in one process share them.
   Fine for both products as they stand, since each process is one product, but it
   constrains tests that would otherwise run in parallel with different prompt sets.

If per-instance prompts are ever needed, the fix is to make the sections a value on
`Config` rather than package variables. Not needed now; worth knowing the exit exists.

> **Separate, pre-existing:** `agentInstance` itself is reassigned at `main.go:1507`
> from the settings-change goroutine while the incident queue (started at 1373) and
> fleet sweep (1377) hold and read it, with no mutex or atomic. That race exists in the
> current code, is unrelated to this refactor, and should be fixed on its own terms.

### Which direction each call goes

```
        ┌──────────────────────────────────────────────┐
        │  Enbarr  cmd/omamori-net, internal/*         │
        └───────────────┬──────────────────────────────┘
                        │
      Enbarr calls IN   │   ~35 exported symbols:
      (compile-time     │   New, Registry, RunDAGSync, Interject,
       import)          │   SetRemoteExec, ParseBootMD, Trigger, ...
                        ▼
        ┌──────────────────────────────────────────────┐
        │  Kaiju   pkg/agent — the engine               │
        └───────────────┬──────────────────────────────┘
                        │
      Kaiju calls OUT   │   ONLY through values Enbarr handed it:
      (runtime, via     │   • a Tool from the registry
       values, never    │   • RemoteExecutor.Execute(...)
       via import)      │   • IPCSender.Send(...)
                        ▼
        ┌──────────────────────────────────────────────┐
        │  Enbarr's implementations                     │
        │  internal/streams (libp2p), internal/ipc,     │
        │  internal/store, internal/agent/builtin       │
        └──────────────────────────────────────────────┘
```

**Kaiju never imports Enbarr.** When the engine runs a security tool, it is calling a
function pointer Enbarr put in the registry at startup. When it dispatches to a remote
machine, it is calling an interface method on a value Enbarr constructed. At compile
time, Kaiju has no idea Enbarr exists.

That is why libp2p never enters Kaiju's `go.mod`: the engine holds an interface, and
the implementation containing libp2p is on Enbarr's side of the boundary.

---

## 3. What happens when you build

### Development

```bash
$ cd /home/sites/omamori/OmamoriNet
$ go build ./cmd/omamori-net
```

1. `go.mod` says `require github.com/Compdeep/kaiju v0.4.0`.
2. `go.work` at `/home/sites/omamori/` says `use ./kaiju`, and `./kaiju` is a symlink
   to your local checkout. The workspace **overrides** the version in `go.mod`.
3. The compiler builds `kaiju/pkg/agent` from your working tree — your uncommitted
   edits included.
4. The compiler builds Enbarr's packages, which import it.
5. It links **one executable**: `omamori-net`.

Edit either tree, rebuild, done. Same speed as editing the fork today.

### Release

```bash
$ GOWORK=off go build ./cmd/omamori-net
```

`GOWORK=off` makes the compiler ignore the workspace entirely. It resolves
`github.com/Compdeep/kaiju v0.4.0` from the module cache — the tagged, published
version. What ships is never whatever happened to be in someone's local checkout.

Put that in `build.sh` so it is not something anyone has to remember.

### What ships

**One binary.** No Kaiju executable. No shared library. No plugin directory. No
runtime dependency of any kind. Kaiju is compiled in, exactly like `go-libp2p` is
today — the installer and the systemd unit do not change at all.

---

## 4. How Kaiju stands on its own afterwards

Kaiju does not become a library that only Enbarr can use. It remains a complete,
runnable product:

```bash
$ cd /home/sites/kaiju/kaiju
$ go build ./cmd/kaiju
$ ./kaiju serve            # web UI on :8080, exactly as today
```

`cmd/kaiju` imports `pkg/agent` — **the same public API Enbarr imports, through the
same seam.** Kaiju's daemon is simply another application embedding the engine:

| | Kaiju's own daemon | Enbarr |
|---|---|---|
| Engine | `pkg/agent` | `pkg/agent` — same code |
| Tools | `internal/tools` (bash, file, git, web) | `internal/agent/builtin` (24 security tools) |
| Prompts | the embedded `prompts.md` defaults | overrides SOUL, PREFLIGHT, EXECUTIVE, AGGREGATOR, CLASSIFIER |
| `RemoteExecutor` | not supplied → remote execution off | libp2p → remote execution on |
| `IPCSender` | not supplied → no IPC | named pipe / unix socket → C++ service |
| `EventStore` | its own sqlite | Enbarr's `events.db` |
| UI | `internal/web` | Enbarr's dashboard |

Neither can reach into the other's tools, prompts or transport. Both go through the
same registry and the same interfaces.

### Why this matters more than it sounds

Because Kaiju's own daemon depends on the public API, **a missing extension point
breaks Kaiju's build in Kaiju's own CI, before Enbarr ever sees it.** You cannot ship
an engine that is not embeddable, because your own product is the first thing
embedding it.

Go's export rules already guarantee the move is safe on the Kaiju side:
`internal/api` importing `internal/agent` can only touch *exported* identifiers today.
Moving the package out of `internal/` changes the import path and nothing else about
what Kaiju's own code can reach.

---

## 5. What each side owns, permanently

| | Kaiju | Enbarr |
|---|---|---|
| DAG planner, scheduler, dispatcher | ✅ owns | imports |
| IGX gate, intent registry | ✅ owns | imports |
| Reflection, observer, micro-planner | ✅ owns | imports |
| Tool *interface* and registry | ✅ owns | imports |
| Prompt sections + override mechanism | ✅ owns defaults | overrides 5 of 19 |
| Security tools | — | ✅ owns |
| libp2p, gossip, roster, peer store | — | ✅ owns |
| C++ service, ETW/eBPF, named pipe | — | ✅ owns |
| Alert store, incidents, triage | — | ✅ owns |
| Web dashboard, 3D topology | — | ✅ owns |

The line is: **Kaiju owns how work is planned and executed. Enbarr owns what the work
is about and how it reaches the machines.**

---

## 6. The one thing that is genuinely hard

Everything above describes the end state, and most of the path to it is moving files.
The exception is `executive.go` and `scheduler.go`.

Those two files hold **2,598 differing lines** — not new functions that can be sorted
into "Kaiju's" and "Enbarr's", but *edits inside the same function bodies*, made
independently by both sides. Because a Go package cannot be split across modules, and
34 of the 42 shared files are in `package agent` itself, they cannot be migrated
gradually. They go across in one step, reconciled by hand.

That is the real cost, it is paid once, and no amount of restructuring avoids it.

---

## See also

- `kaiju-integration-scope.md` — the phase plan, the measurements, the risks
- `kaiju-seam-and-ledger.md` — the 60-item symbol ledger and the seam's exact contents
