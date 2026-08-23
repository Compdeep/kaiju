# Embedding the engine

Building an application on kaiju: what you construct, what you implement, and
where your own logic goes.

This is about the Go types — `agent.Config`, `agent.Handlers`, `toolapi.Tool`.
For the JSON file the `kaiju` binary reads, see [config.md](config.md).

## The smallest working agent

```go
ag, err := agent.New(agent.Config{
    ModelConfig: agent.ModelConfig{
        LLMEndpoint: "https://api.openai.com/v1",
        LLMAPIKey:   os.Getenv("OPENAI_API_KEY"),
        LLMModel:    "gpt-4o",
        MaxTokens:   4096,
    },
    PathConfig:     agent.PathConfig{Workspace: dir, DataDir: dir, MetadataDir: dir},
    IdentityConfig: agent.IdentityConfig{NodeID: "this-node"},
    DAGConfig:      agent.DAGConfig{DAGEnabled: true, MaxNodes: 20, MaxLLMCalls: 20},
    RoutingConfig:  agent.RoutingConfig{ClassifierEnabled: true},
})
if err != nil {
    return err
}
go ag.Start(ctx)

res, err := ag.SubmitSync(ctx, agent.Trigger{
    Type: "chat_query",
    ID:   "req-1",
    Data: json.RawMessage(`{"query":"what is listening on this host?"}`),
})
fmt.Println(res.Outcome)
```

That is a complete agent. It classifies the request, plans steps, runs tools,
reflects on what came back, and writes an answer. **No `Handlers` are supplied**
— kaiju's own binary supplies none either. `New` rejects exactly one omission,
an empty `MetadataDir`; everything else has a default or a feature that stays
off.

## Giving it tools

A tool is four methods, plus one more if you want a typed result.

```go
type ProcessList struct{}

func (ProcessList) Name() string              { return "process_list" }
func (ProcessList) Description() string       { return "List running processes." }
func (ProcessList) Impact(map[string]any) int { return toolapi.ImpactObserve }
func (ProcessList) Parameters() json.RawMessage {
    return json.RawMessage(`{"type":"object","properties":{}}`)
}

// Preferred: return an envelope. The engine reads its Status to tell an empty
// result from a failure, and its payload feeds ${node.<id>.field} references in
// later steps.
func (p ProcessList) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
    lines, err := readProcesses()
    if err != nil {
        return toolapi.ToolFail("listing", err.Error(), nil), nil
    }
    if len(lines) == 0 {
        return toolapi.ToolEmpty("listing", "no processes matched"), nil
    }
    return toolapi.ToolOK("listing", strings.Join(lines, "\n"),
        map[string]any{"count": len(lines)}), nil
}

func (p ProcessList) Execute(ctx context.Context, params map[string]any) (string, error) {
    return toolapi.StringResult(p.ExecuteTyped(ctx, params))
}

ag.Registry().Register(ProcessList{})
```

**Say which of `ok` / `empty` / `error` it was.** A tool that returns "no
results found" with status `ok` looks like a success to everything downstream —
the run's account of itself, the answer, the coverage of the request. That
distinction is the main thing the envelope buys.

**`Impact` is the authority the call needs**, and the engine's gate refuses a
call whose impact exceeds the run's intent. `ImpactObserve` reads, higher ranks
change things.

## Handlers — your logic, at the engine's decision points

Fourteen fields. **Every one is optional; nil means the question is never
asked.** An application that never sends work to another machine leaves `Remote`
unset, and a step naming a machine is refused rather than run in the wrong
place.

```
  a run is about to start ──► Admit            may this run at all
                          ──► Unattended       is anybody watching
                          ──► TokenCategory    whose budget is this
  preflight has decided   ──► Refine           correct my reading of it
  a prompt is assembled   ──► Environment      describe the surroundings
                          ──► DescribeTrigger  say what started this
  a step is about to run  ──► ValidateTarget   is that machine name real
                          ──► AllowTool        your rules, after my gate
                          ──► Clearance        has this principal the authority
                          ──► Remote           run it over there
  a step has run          ──► Audit            record the decision
  the run ends            ──► Answer           write the verdict yourself
                          ──► Store            record the run and its actions
  the planner asks        ──► RunTargets       which machines exist
```

The one most applications write first:

```go
Handlers: agent.Handlers{
    // Asked after the engine's own gate has passed the call, so it can only
    // narrow. A refusal is handed to the model as the call's result, not as an
    // error — so it reads why and does something else instead of retrying.
    AllowTool: func(ctx context.Context, req agent.ToolCallRequest) (bool, string) {
        if req.Tool == "delete_everything" && req.Target != "" {
            return false, "that tool may only run on this machine"
        }
        return true, ""
    },
},
```

`req` carries the tool, the parameters, the target, the trigger and the graph.
Writing to `req.Params` changes the call.

**Every call into your code goes through a recover.** A panic in a handler fails
that step and leaves the run standing; it never ends the process.

**Two more worth knowing.** `Answer` replaces the built-in answer writer, so
your application produces its own structured verdict instead of prose — it
returns an `*AnswerResult` whose `Data` the engine carries back to you untouched
in `SyncResult.Data`. `Store` receives each finished run and each action taken,
which is how you persist what happened.

## Setters — configuration, arriving late

Eleven methods on `*Agent` change a value after construction, and three more do
work rather than set a value.

**A handler and a setter are not two versions of one thing.** A handler takes an
argument and the answer depends on it — `AllowTool(ctx, req)` cannot be
precomputed, because the answer is about *that* call. A setter takes no
question: `SetChatModel("openai", "gpt-4o")` puts a string in a field, and the
value is the same for every run that reads it.

So a handler is a function because the answer varies with the question; a
setting is a value because it does not. That is why a setting is also a `Config`
field and a handler cannot be.

**Which means most setters are a second door onto the same field.**
`applyModels` fills `a.llm`, `a.executor`, `a.visionModel`, `a.chatModel`,
`a.routeModel` and `a.answerModel` straight from `Config`. The only thing a
setter does that a `Config` field cannot is say it again, to an agent that is
already running — which is what a dashboard settings change needs.

**If you are embedding, prefer `Config`.** Reach for a setter only when an
operator changes something on a running service and it must take effect without
a restart. Every setter that survives says in its own doc comment what reads it
during a run, and the `Config` field it writes says which setter reaches it:

```go
    // Set at run time: SetAnswerModel.
    AnswerProvider, AnswerModel string
```

So you can answer "can this still be changed after `New`" from the field itself,
which matters because there is no way to find out by trying: `New` takes `Config`
by value, so setting a field on your own copy afterwards fails silently.

Nine of the eleven have a field behind them. `SetToolReach` and
`SetClearanceChecker` are the two that do not — the first changes a tool's reach
in the registry, the second installs the checker asked on every gated call, and
neither is something `Config` holds.

Two guards keep the two files together: a setter whose comment cannot say what
reads it mid-run fails the build, and so does a note naming a setter that no
longer exists.

**Three do work rather than set a value**, take a context, and are called before
the agent starts: `InitSkills` loads skill files, `InitEmbeddings` builds the
embedding client and loads its store, `InitKernel` builds the kernel. `Start`
calls `InitKernel` itself — the public method is for a one-shot path that never
calls `Start`.

## The whole shape

```
                        your application
                              │
                              │  one Config, one call
                              ▼
  ┌──────────────────────────────────────────────────────────────┐
  │  agent.New(Config{                                           │
  │      ModelConfig · PathConfig · IdentityConfig                │  facts,
  │      DAGConfig   · RoutingConfig · ComputeConfig              │  stated once
  │      Handlers{ … }  ────────────────────────────┐            │
  │  })                                             │            │
  └─────────────────────────────────────────────────┼────────────┘
                              │                     │
                              ▼                     │
                          *Agent  ◄─────────────────┘
                              │      each handler copied to a private field;
                              │      two guards fail if one is added without
                              │      a wiring line or a panic wrapper
                              │
         ┌────────────────────┴────────────────────┐
         │                                         │
         ▼                                         ▼
   DURING A RUN                            AFTER CONSTRUCTION
   the engine asks, with                   you tell, with no
   the thing being decided                 question attached

   admit(trigger)      ─► Admit            ag.SetChatModel(…)
   allowTool(req)      ─► AllowTool        ag.SetConcurrency(…)
   checkClearance(…)   ─► Clearance        ag.SetToolReach(…)
   writeAnswer(…)      ─► Answer           ag.InitSkills(…)
   audit(entry)        ─► Audit
   remoteExecute(req)  ─► Remote           the same fields Config
   …                                       already fills — said again,
                                           later, to a running agent
   each through a recover,
   so your code cannot end
   the process
```

## Adding a handler to the engine

If you are extending kaiju itself rather than embedding it, a new handler is
four things and two of them fail loudly if forgotten.

1. A field on `Handlers`, with a comment saying what nil means.
2. A line in `applyHandlers` copying it to a private field —
   `handlers_wiring_test.go` fails without it.
3. A wrapper method that calls it inside a `recover` — `panic_test.go` fails
   without it.
4. An entry in each of those two tables, naming the field and the wrapper.

## Where to look next

- [graph.md](graph.md) — how a run is planned, executed and reflected on
- [tools.md](tools.md) — the tool contract in full, including output schemas
- [model-calls.md](model-calls.md) — lanes, reply sizing, and the one door
- [authorization.md](authorization.md) — intent, impact and clearance
