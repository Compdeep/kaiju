package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

// ── Scheduler: DAG execution engine ─────────────────────────────────────────
//
// Node roles:
//   Executive      — LLM call that decomposes user query into a DAG of steps
//   Tool          — executes a registered tool (bash, file_read, web_search, etc)
//   Compute       — LLM code generation: deep=architect, shallow=coder
//   Reflection    — checkpoint that evaluates evidence and decides continue/investigate/conclude
//   Observer      — per-node LLM evaluator (orchestrator mode only)
//   MicroPlanner  — lightweight LLM that repairs a single failed node
//   Interjection  — human message injected mid-investigation
//   Aggregator    — final LLM synthesis of all evidence into a user-facing response
//   Actuator      — executes follow-up actions recommended by the aggregator
//
// Compute pipeline phases (grafted by scheduler when architect returns):
//   Phase 1: Setup      — sequential bash nodes (scaffold, install deps, create dirs)
//   Phase 2: Coders     — parallel compute(shallow) nodes, one per architect task
//   Phase 3: Execute    — per-coder bash/service nodes (build, start server, apply schema)
//   Phase 4: Validators — parallel bash checks proving the goal was achieved
//
// Failure handling:
//   - Nodes with pending dependents use the retry proxy pattern: stay "running"
//     while micro-planner retries, dependents blocked until success or retries exhaust
//   - Leaf nodes (no dependents) fail normally, errors flow as evidence to reflector
//   - After a reflector investigate decision and Holmes-led fix, stored validators are re-grafted to verify the fix
//
// ─────────────────────────────────────────────────────────────────────────────

/*
 * setupDAGPipeline creates graph, budget, observer and wires SSE streaming.
 * desc: Initializes the investigation graph and budget from agent config,
 *       sets up the observer channel for live event streaming, registers
 *       the graph with the agent for SSE subscribers, and returns a cleanup
 *       function that must be deferred.
 * param: trigger - the run's trigger.
 * param: runID - the identity stamped on the run's context, so the graph and
 *        every trace written before it existed agree.
 * return: graph, budget, and cleanup function.
 */
func (a *Agent) setupDAGPipeline(trigger Trigger, runID string) (*Graph, *Budget, func()) {
	graph := NewGraph()
	graph.RunID = runID
	// Resolved once, here, because every reader of a payload is downstream of
	// this and none of them has an Agent to ask.
	graph.payloadCap = a.budget(payloadBudget)
	budget := NewBudget(
		a.cfg.MaxNodes,
		a.cfg.MaxPerSkill,
		a.cfg.MaxLLMCalls,
		a.cfg.MaxObserverCalls,
		a.cfg.DAGWallClock,
	)

	// Construct the per-investigation ContextGate. This is the single API
	// every prompt builder uses to fetch context. Lives on the graph so it
	// dies cleanly when the investigation ends.
	graph.Context = NewContextGate(graph, &trigger, a)

	// Tag this investigation's graph with its session so every event it emits is
	// routable per-session — no shared dagSessionID that a concurrent run could
	// clobber. broadcastDAGEvent stamps SessionID from the graph at emission.
	graph.SessionID = trigger.SessionID

	observerCh := make(chan DAGEvent, 128)
	graph.SetObserver(observerCh)
	go a.dagFanOut(observerCh, graph)
	a.broadcastDAGEvent(graph, DAGEvent{Type: "start", TriggerID: trigger.ID, RunID: graph.RunID, SessionID: trigger.SessionID, Targets: a.runTargets(trigger), Nodes: graph.Snapshot()})

	cleanup := func() {
		a.broadcastDAGEvent(graph, DAGEvent{Type: "done", TriggerID: trigger.ID, RunID: graph.RunID, SessionID: trigger.SessionID, Nodes: graph.Snapshot()})
		close(observerCh)
	}

	return graph, budget, cleanup
}

/*
 * runPlanAndSchedule runs phases 1-2: executive then scheduler loop.
 * desc: Returns nil error if all nodes complete (even if some failed/skipped).
 *
 *       The scheduler loop is mode-aware but structurally identical:
 *         1. Launch all ready nodes (deps satisfied)
 *         2. Wait for a completion
 *         3. Handle the completion based on node type:
 *            - Tool: record result, then mode-specific post-processing
 *            - Reflection/Interjection: parse decision (continue/replan/conclude)
 *            - Observer: parse action (continue/inject/cancel/reflect)
 *            - MicroPlanner: graft replacement nodes for failed tools
 *         4. Check for human interjection (all modes)
 *         5. Launch newly ready nodes
 *         6. Repeat until no inflight nodes remain
 *
 *       Mode-specific behavior happens at step 3 only:
 *         - reflect: reflections are structural (injected by executive, gate downstream)
 *         - nReflect: reflection injected every BatchSize tool completions
 *         - orchestrator: per-node orchestrator LLM spawned after each tool completes
 *
 *       Returns the resolved intent (which may differ from trigger.Intent() when
 *       the executive auto-infers it). A pre-aggregator reflection always fires at
 *       the end to evaluate results before aggregation.
 * param: ctx - context for the investigation (with wall clock timeout).
 * param: trigger - the investigation trigger.
 * param: graph - the investigation graph.
 * param: budget - the execution budget.
 * return: resolved IGX intent and error.
 */
// replanFrameTemplate is the frame handed to the executive on a replan (%s = the
// reflector's `next`). It MUST keep teaching step wiring — leading with a
// web_search→web_fetch chain (`${step.0.results.0.url}`, `depends_on:[0]`) — and
// MUST NOT tell the planner to avoid `${step}`/`depends_on` for its new steps.
// A prompt that bans wiring here is exactly what collapsed replans to flat plans
// with literal URLs invented from memory (guarded by TestReplanFrame_TeachesWiring).
//
// What it carries is what is true of EVERY re-plan: the plan so far has run, and
// this is how new steps are wired. Anything true only of some re-plans is added
// at the call site — see replanDebugParagraph.
const replanFrameTemplate = "\n\n## Re-plan\nThe plan so far has already run — the worklog below (## System State) shows completed work. Do NOT repeat completed steps.\n\nReflector says the next move is:\n%s\n\nPlan the next steps needed to close what remains and answer the original request above. WIRE your new steps into a chain, exactly like a first plan — e.g. a `web_search` tagged `find_docs`, then a `web_fetch` whose url param is `${step.find_docs.results.0.url}`. The reference IS the wiring; do not also write `depends_on`. A reference addresses the NEW steps in THIS plan, by their tags. A step that already RAN is not addressable from here — neither its position nor its tag reaches back, because both name steps in THIS plan. What it returned is above, as a tool result: take the value out of that and write the value itself into the param, with `depends_on:[]`. A value you write literally must be one you can point to in the material above — a tool result, or the request itself. Copying it from there is what the sentence before this one asks for. What you may not write is a value you recall or assume: a URL, an id or a path you cannot find above does not exist, and the step that produces it is what to plan instead."

// replanDebugParagraph is added to the frame only when something has actually
// failed.
//
// It used to be part of the frame itself, so every re-plan was told how to
// diagnose a failure whether or not one had happened. A run that had simply not
// finished gathering was handed a paragraph about exact error text and module
// names, which is advice for a situation it was not in — and the more of the
// frame describes a situation the planner is not in, the less of it reads as
// being about this run.
const replanDebugParagraph = "\n\nIf the next move is to FIX a FAILURE, plan a single `debug` step (a leaf, no dependents) with the failure — exact error text, file paths, module names — in its `problem` param; the debugger diagnoses the root cause and applies the fix, and the following re-plan handles any follow-on work."

// complexFanoutFloor is the number of resolved tool steps at or above which a run
// counts as "complex" regardless of what preflight guessed — a structural backstop
// so a query that actually fanned out to many gather steps still earns a full
// synthesis. A simple lookup resolves 1-3 tool nodes; a real research run resolves
// many more.
const complexFanoutFloor = 6

// triggerIsAwaited reports whether something is waiting on this run's answer.
//
// A person typing, or a caller holding a request open. The alternative is work
// that started on a schedule or an event, where a short "nothing to report" is
// a fine answer and an unnecessary synthesis pass is just cost.
func triggerIsAwaited(t Trigger) bool {
	switch t.Type {
	case "chat_query", "api_query", "command":
		return true
	}
	return false
}

/*
 * isModelStage reports whether a node's failure could be OUR credentials being
 * rejected.
 * desc: A stage that called a model can report an auth failure about this
 *       deployment. A tool cannot: its 401 or 403 belongs to whatever it was
 *       talking to, and reading it as ours stops the run on somebody else's
 *       access control.
 * param: n - the node that failed.
 * return: true when the failure could be about our own credentials.
 */
func isModelStage(n *Node) bool {
	if n == nil {
		return false
	}
	switch n.Type {
	case NodeTool, NodeActuator:
		return false
	}
	return true
}

/*
 * aggregatorWillWriteTheAnswer reports whether a stage after the reflector will
 * produce the reply, so the reflector need not.
 * desc: The reflector is asked for an "outcome" — the final answer for the user
 *       — every time it concludes. On the paths where the aggregator runs, that
 *       answer is then thrown away and the aggregator's is used instead. On an
 *       interactive query that is EVERY time: decideAutoAggMode's first branch
 *       is "someone is waiting on this answer, so it is synthesised whatever the
 *       reflector said".
 *
 *       So the reflector spends its reply budget, and the seconds with it,
 *       writing something nothing reads. Asked before the call, this says
 *       whether that is about to happen.
 *
 *       It answers only when it is CERTAIN. A pinned agg_mode of 0 means the
 *       reflector's outcome IS the answer — an embedding application that reads
 *       the run's outcome directly depends on it being written — and in auto
 *       mode the reflector's own aggregate flag has not been written yet, so
 *       decideAutoAggMode is asked with nil and its fallback is 0. Both resolve
 *       to "no", which keeps the answer being written. Only a mode already
 *       committed to aggregating returns true.
 * param: trigger - the run's trigger, which may pin the mode.
 * param: graph - the run so far.
 * return: true when the reply is certainly being written by a later stage.
 */
func (a *Agent) aggregatorWillWriteTheAnswer(trigger Trigger, graph *Graph) bool {
	switch mode := trigger.AggMode; {
	case mode == 0:
		return false // the reflector's outcome is the answer
	case mode > 0:
		return true // pinned to an aggregator lane
	}
	// Auto. Every input but the reflector's own flag is already settled, and
	// that flag is consulted last — so asking now, with nil, gives the same
	// answer as asking later on every branch that does not depend on it.
	needsSynthesis := graph.Preflight != nil && graph.Preflight.NeedsSynthesis
	complex := needsSynthesis || a.runFanout(graph) >= complexFanoutFloor
	mode, _ := decideAutoAggMode(
		graph.HasNodeOfType(NodeCompute), complex, a.hasUsableEvidence(graph),
		triggerIsAwaited(trigger), nil)
	return mode > 0
}

/*
 * preflightSummary is the one line the trace shows for what preflight decided.
 * desc: What it settled, in the terms the rest of the run uses: which lane, what
 *       the step is allowed to do, and which guidance was picked. Enough for a
 *       reader to tell a chat turn from an agent one before any step has run.
 * param: pf - the result. Nil gives an empty line rather than a panic.
 * return: the line, or "".
 */
func preflightSummary(pf *PreflightResult) string {
	if pf == nil {
		return ""
	}
	out := pf.Mode
	if out == "" {
		out = "agent"
	}
	out += " · " + pf.Intent.String()
	if len(pf.Skills) > 0 {
		out += " · " + strings.Join(pf.Skills, ", ")
	}
	return out
}

// decideAutoAggMode picks the aggregator lane in auto mode (agg_mode -1): 0=skip
// (use the reflector's outcome), 1=executor model, 2=reasoning model. Pure over the
// structural signals so it is unit-tested directly (TestDecideAutoAggMode).
//
//   - someone is waiting        → 2 (never skip on a run a person asked for)
//   - compute present            → 1 (a compute run always needs a formatted answer)
//   - complex + usable evidence   → 2 (a real synthesis, with the honesty framing)
//   - complex + NO usable evidence→ 0 (nothing to synthesize; the reflector's honest
//     "couldn't get the data" outcome stands — this is the reflector's override)
//   - simple + reflector wants it → 2
//   - simple, reflector done      → 0
//
// The first case is why awaited is a parameter. Skipping is a saving, and it is
// paid for by whoever reads the answer: the reflector writes a terse summary
// that throws away the tool output the question was about. That is acceptable
// for a run nothing is waiting on, and not for one a person asked for.
func decideAutoAggMode(hasCompute, complex, hasEvidence, awaited bool, reflectorWants *bool) (int, string) {
	switch {
	case awaited:
		return 2, "someone is waiting on this answer, so it is synthesised whatever the reflector said"
	case hasCompute:
		return 1, "compute nodes present, forcing aggregator"
	case complex && hasEvidence:
		return 2, "complex query → aggregating"
	case complex:
		return 0, "complex query but no usable evidence — using reflector's outcome"
	case reflectorWants != nil && *reflectorWants:
		return 2, "reflector requested aggregation (reasoning model)"
	default:
		return 0, "reflector outcome is complete, skipping aggregator"
	}
}

// scheduleOutcome holds the outcome of plan+schedule, including an optional outcome
// from reflection that can skip the aggregator.
type scheduleOutcome struct {
	Intent              gates.Intent
	ReflectionOutcome   string // non-empty if reflection concluded with a full outcome
	ReflectionAggregate *bool  // reflector's recommendation: true = needs aggregator, false = outcome is complete
}

// scaleReplanCap raises the replan ceiling to match the difficulty the executive
// revealed in its INITIAL plan — the plan itself is the difficulty signal, so no
// extra LLM call and no self-estimate. `base` (the configured cap) is a floor;
// bigger plans and compute steps (code generation / project scaffolding) each add
// headroom, clamped to a hard ceiling so a huge plan can't uncap the run.
//
// A 2-step lookup keeps the base; a 20-step or compute-heavy build earns several
// more expand rounds. Note the replan cap is only one budget lever — wall clock
// and LLM/node budgets still bound the run independently.
func scaleReplanCap(base int, steps []PlanStep) int {
	extra := len(steps) / 4
	for _, s := range steps {
		if s.Tool == "compute" || s.Type == "compute" {
			extra++
		}
	}
	ceiling := 12
	if base > ceiling {
		ceiling = base
	}
	capped := base + extra
	if capped > ceiling {
		capped = ceiling
	}
	return capped
}

func (a *Agent) runPlanAndSchedule(ctx context.Context, trigger Trigger, graph *Graph, budget *Budget) (*scheduleOutcome, error) {
	// Inject data directory override into context for retrieval tools (forwarded runs)
	if trigger.DataDir != "" {
		ctx = toolapi.WithDataDir(ctx, trigger.DataDir)
	}
	// Propagate session ID onto the graph so compute nodes can resolve
	// per-session interfaces.json without threading trigger through every layer.
	graph.SessionID = trigger.SessionID

	// Resolve DAG mode: trigger-level override (from frontend) > config default
	dagMode := a.cfg.DAGMode
	if trigger.DAGMode != "" {
		dagMode = trigger.DAGMode
	}

	// ── Phase 0: Preflight (two separable jobs) ──
	//   routing   — mode = chat / meta / investigate; chat & meta short-circuit the
	//               executive. Only needed in interactive mode.
	//   plan-prep — skills (which guidance cards), intent (rank), required_categories,
	//               and a context paragraph. The agent's own pre-plan setup.
	// Autonomous mode is pure agent: skip routing (its result would only be
	// discarded) and run plan-prep directly. Interactive mode routes first, and
	// short-circuits chat/meta before paying for plan-prep.
	if a.cfg.ClassifierEnabled {
		if !budget.TrySpawnNode("", true) {
			return nil, fmt.Errorf("budget exhausted before preflight")
		}
		execMode := a.cfg.ExecutionMode
		if trigger.ExecutionMode != "" {
			execMode = trigger.ExecutionMode
		}
		query := a.formatTrigger(trigger)

		// A row on screen before anything has run.
		//
		// Preflight is two model calls and takes several seconds, and both are
		// pre-graph — no node, no event — so the trace element does not render
		// at all until the first real node appears. From the user's side the
		// interface simply sits there. This says what is happening while it is
		// happening; the node is synthetic, like the executive's and the
		// aggregator's.
		a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: "preflight", Node: &NodeInfo{
			ID: "preflight", Type: "preflight", State: "running", Tag: "reading the request"}})

		var pf *PreflightResult
		if execMode == "autonomous" {
			// Pure agent: no routing (its result would only be discarded). Prepare
			// the plan directly, so autonomous always has full skills/context.
			pf = a.classifyInvestigate(ctx, trigger.ID, query, trigger.History)
			pf.Mode = "agent"
		} else {
			// Interactive: route first; chat short-circuits before plan-prep, agent plans.
			mode, _ := a.routeQuery(ctx, trigger.ID, query, trigger.History)
			switch mode {
			case "chat":
				pf = &PreflightResult{Mode: "chat"}
			default: // "agent"
				pf = a.classifyInvestigate(ctx, trigger.ID, query, trigger.History)
			}
		}
		// Preflight's two answers have to agree with each other, and whether it
		// may adjust one of them depends on the run — so it happens here, where
		// the trigger is, rather than inside the validation of the model's reply.
		a.reconcileComputeIntent(trigger, pf)

		a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: "preflight", Node: &NodeInfo{
			ID: "preflight", Type: "preflight", State: "resolved", Tag: "reading the request",
			Summary: preflightSummary(pf)}})

		// Per-investigation preflight + card list live on the Graph, not the
		// Agent, so concurrent investigations never clobber each other's state.
		graph.Preflight = pf
		graph.ActiveCards = pf.Skills

		// The decision the whole run is gated on, recorded where it lands rather
		// than where it was made: preflight runs before the graph has a plan,
		// and this is the first point at which both exist. See debugrecord.go.
		//
		// It is the intent that matters most here. A preflight whose reply would
		// not parse falls back to observe-only, and every compute step in the
		// plan is then refused — with nothing anywhere saying that a decision was
		// lost rather than made.
		if pfOut, mErr := json.Marshal(pf); mErr == nil {
			graph.recordStage(DebugRecord{
				ID: "preflight", Kind: "preflight", Label: "classify", Out: pfOut,
				Text: pf.Context.Text(),
			})
		}

		log.Printf("[dag] preflight: mode=%s intent=%s skills=%v categories=%v context=%q",
			pf.Mode, pf.Intent, pf.Skills, pf.RequiredCategories, Text.TruncateLog(pf.Context.Text(), 120))

		// Short-circuit chat — skip the executive entirely. Interactive only;
		// autonomous never produces this mode.
		if pf.Mode == "chat" {
			log.Printf("[dag] preflight short-circuit: mode=%s, skipping planner", pf.Mode)
			return nil, &ExecutiveConversationalError{Text: ""}
		}
	}

	// The application refines what preflight concluded, using facts this package
	// does not have — or replies with a question when the request cannot be
	// acted on as written. Before the planner, so a question costs one cheap
	// call rather than a plan and a set of tool runs.
	//
	// A reply travels as a conversational result, the same path a direct answer
	// takes: the run ends, the question is the answer, and the user's next
	// message continues the thread with the exchange in History.
	if graph.Preflight != nil {
		refined, reply := a.refinePreflight(ctx, graph.Preflight, &trigger)
		if reply != "" {
			log.Printf("[dag] preflight refinement replied instead of planning: %s", Text.TruncateLog(reply, 200))
			a.broadcastDAGEvent(graph, DAGEvent{Type: "outcome", Text: reply})
			return nil, &ExecutiveConversationalError{Text: reply}
		}
		if refined != graph.Preflight {
			graph.Preflight = refined
			graph.ActiveCards = refined.Skills
		}
	}

	// ── Phase 1: Planner ──
	if !budget.TrySpawnNode("", true) {
		return nil, fmt.Errorf("budget exhausted before planner")
	}
	a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: "executive", Node: &NodeInfo{ID: "executive", Type: "executive", State: "running", Tag: "plan"}})

	planResult, err := a.runExecutive(ctx, trigger, graph)
	if err != nil {
		// Conversational response (trivial query) — not a real failure
		var convErr *ExecutiveConversationalError
		if errors.As(err, &convErr) {
			a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: "executive", Node: &NodeInfo{ID: "executive", Type: "executive", State: "resolved", Tag: "direct answer"}})
			if convErr.Text != "" {
				a.broadcastDAGEvent(graph, DAGEvent{Type: "outcome", Text: convErr.Text})
			}
			return nil, err
		}
		a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: "executive", Node: &NodeInfo{ID: "executive", Type: "executive", State: "failed", Tag: "plan", Error: err.Error()}})
		return nil, fmt.Errorf("planner failed: %w", err)
	}
	a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: "executive", Node: &NodeInfo{
		ID: "executive", Type: "executive", State: "resolved", Tag: "plan",
		Tools: planResult.Tools, Objective: planResult.Objective}})

	initialNodes, err := planStepsToNodes(planResult.Steps, graph, budget, a.registry, dagMode)
	if err != nil {
		return nil, fmt.Errorf("plan-to-nodes failed: %w", err)
	}

	// Refused before anything runs, when the caller pinned a rank the plan
	// cannot be done at. Discovered mid-run it costs every step up to the first
	// one over the line, and arrives as an arithmetic comparison with no remedy
	// in it — see IntentGapError.
	if gap := a.validatePlanIntent(initialNodes, trigger, trigger.Intent()); gap != nil {
		log.Printf("[dag] %s", gap.Error())
		a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: "executive", Node: &NodeInfo{
			ID: "executive", Type: "executive", State: "failed", Tag: "plan", Error: gap.Error()}})
		return nil, gap
	}

	log.Printf("[dag] plan: %d nodes created", len(initialNodes))

	// ── Phase 2: Scheduler loop ──
	completionCh := make(chan nodeCompletion, 64)
	inflight := 0
	throttle := newToolThrottle()
	batchCounter := 0             // nodes resolved since last reflection (nReflect mode)
	reflectionConcluded := false  // true if a reflection already decided "conclude"
	reflectionOutcome := ""       // full outcome from reflection (skips aggregator if non-empty)
	var reflectionAggregate *bool // reflector's recommendation on aggregation
	reflectionInflight := false   // true while a reflection node is running — prevents double injection
	workSinceReflection := 0      // tool nodes completed since last reflection — used to ensure final reflect
	// debugGraftPending tracks debugger-grafted nodes that haven't reached a
	// terminal state yet. Reflection injection waits for this to reach 0
	// before firing so we don't re-read stale failure state mid-fix.
	debugGraftPending := 0
	debugGraftIDs := make(map[string]bool)
	// debuggerInflight is true while a Holmes or microplanner cycle is
	// running. Both batch reflection and quiescence reflection hold while
	// this is set. The reflector is the sync point — it clears the flag at
	// fire time. Declared up here (not next to the main loop) so the
	// injectBatchReflection closure below can capture it.
	debuggerInflight := false

	// addressingByInvestigation tracks which failed node IDs each investigation cycle is
	// addressing. Snapshotted at Holmes dispatch (when failures are still
	// fresh) and consumed at microplanner dispatch (after Holmes concludes).
	// Keyed by investigationCount.
	addressingByInvestigation := make(map[int][]string)

	// IGX: resolve intent — use planner-inferred intent for auto, else structural
	intent := trigger.Intent()
	if planResult.WasAuto {
		intent = planResult.InferredIntent
		// Cap inferred intent by clearance (planner can't escalate beyond node's ceiling)
		clr := gates.Intent(a.clearance.Clearance())
		if intent > clr {
			log.Printf("[dag] executive inferred %s but clearance is %d, capping", intent, clr)
			intent = clr
		}
		// Cap by user's scope ceiling (planner can't escalate beyond what
		// the user is allowed to request)
		if trigger.Scope != nil && gates.Intent(trigger.Scope.MaxIntent) < intent {
			log.Printf("[dag] executive inferred %s but user scope caps at %d, capping", intent, trigger.Scope.MaxIntent)
			intent = gates.Intent(trigger.Scope.MaxIntent)
		}
	}
	log.Printf("[dag] scheduler mode: %s, intent: %s", dagMode, intent)

	// launchReady fires all ready nodes.
	// retryOnce gives a step that failed one repair attempt, and says whether it
	// made one.
	//
	// A tool reports failure two ways — a Go error, or an envelope whose status is
	// error — and only the first reached this. bash reports the second, so the
	// engine's most-used tool was also its only unrepairable one: a run asking for
	// privilege escalation had eight bash steps fail and not one of them was tried
	// again. Both ways arrive here now, because a tool that says it failed in the
	// second way is no less repairable than one that said it in the first.
	//
	// detail is what the step actually reported, not the sentence the engine wrote
	// about it. "command failed: exit 1: exit status 1" says nothing a fixer can
	// act on; the stderr beside it said "the input device is not a TTY", which
	// names the flag to drop.
	retryOnce := func(node *Node, comp nodeCompletion, detail string) bool {
		tier := classifyRetryTier(detail)
		// Once per node. The tier that already ran is on the node, not spelled
		// into its name — see Node.Retry.
		if tier == "skip" || node.Retry != "" {
			return false
		}
		if node.Type != NodeTool || node.ToolName == "service" || node.Source == "holmes" {
			// Holmes is investigating: a failure is an observation, and rewriting
			// the command would invalidate what it was asking.
			return false
		}
		switch tier {
		case "blind":
			graph.SetRetry(comp.NodeID, "blind")
			node.Error = nil
			// A host that said "too many requests" is not rerun on the spot.
			// The wait is served in the node's own goroutine, so the rest of the
			// plan carries on meanwhile — see fireNode.
			graph.HoldUntil(comp.NodeID, retryBackoff(detail))
			graph.SetState(comp.NodeID, StatePending)
			log.Printf("[dag] blind retry for %s: %s", comp.NodeID, Text.TruncateLog(detail, 200))
			appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "BLIND_RETRY", detail)
			a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: comp.NodeID, Node: graph.SnapshotNode(comp.NodeID)})
			return true
		case "twotime":
			if budget.LLMRemaining() <= 0 {
				return false
			}
			// The node stays failed while the retry runs, so dependents are not
			// held up by a repair that may not come.
			//
			// The ERROR is cleared, as the blind tier does. SetResult marks a
			// node resolved and records its result but leaves Error untouched,
			// so a retry that succeeds otherwise renders with its own output
			// beside the failure it just recovered from — observed as a node
			// showing "command failed: exit 1" next to exit_code 0 and 333
			// bytes of real output. On the paths where the retry does NOT
			// recover, twotimeRetry reports the original error explicitly, so
			// clearing here loses nothing.
			node.Error = nil
			graph.SetRetry(comp.NodeID, "twotime")
			inflight++
			go a.twotimeRetry(ctx, node, comp, graph, budget, completionCh, detail, intent, trigger.Scope)
			log.Printf("[dag] twotime retry for %s: %s", comp.NodeID, Text.TruncateLog(detail, 200))
			appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "TWOTIME_RETRY", detail)
			a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: comp.NodeID, Node: graph.SnapshotNode(comp.NodeID)})
			return true
		}
		return false
	}

	launchReady := func() {
		for _, n := range graph.ReadyNodes() {
			graph.SetState(n.ID, StateRunning)
			n.StartedAt = time.Now()
			inflight++
			// Broadcast node running event
			a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: n.ID, Node: graph.SnapshotNode(n.ID)})
			if n.Type == NodeReflection {
				// Reflector is the sync point for the debug-cycle flag. Fix 2
				// guarantees no reflection fires while debuggerInflight is true,
				// so by the time we get here the previous Holmes/microplanner
				// cycle is provably done. Clearing once here removes the need
				// for scattered clears in every completion handler.
				debuggerInflight = false
				// Build deterministic reflection context via ContextGate.
				rCtxResp, rctxErr := graph.Context.Get(ctx, ContextRequest{
					ReturnSources: Sources(
						NodeReturns("all"),
						Worklog(10, "all"),
					),
					MaxBudget: 24000,
				})
				if rctxErr != nil {
					log.Printf("[dag] launchReady reflection context build failed: %v", rctxErr)
					rCtxResp = &ContextResponse{Sources: map[string]string{}}
				}
				reflectionInflight = true
				go a.fireReflection(ctx, n, graph, budget, completionCh, trigger, rCtxResp, intent)
			} else if n.Type == NodeHolmes {
				// Holmes iterations carry their own state in params and do
				// not go through the normal tool dispatcher. Before firing,
				// stitch action observations into the prior turn so the next
				// iteration sees them. LastActionNodeIDs lists all parallel
				// action nodes from the previous iteration.
				if state, err := loadHolmesState(n); err == nil && len(state.History) > 0 {
					lastIdx := len(state.History) - 1
					actionIDs := state.LastActionNodeIDs
					// Migration: if old single-ID field is set, use it
					if len(actionIDs) == 0 && state.LastActionNodeID != "" {
						actionIDs = []string{state.LastActionNodeID}
					}
					if len(actionIDs) > 0 && len(state.History[lastIdx].Observations) == 0 {
						obs := make([]string, len(actionIDs))
						for i, aid := range actionIDs {
							if depNode := graph.Get(aid); depNode != nil {
								obs[i] = depNode.Result
								if obs[i] == "" && depNode.Error != nil {
									obs[i] = "ERROR: " + depNode.Error.Error()
								}
							}
						}
						state.History[lastIdx].Observations = obs
						_ = saveHolmesState(n, state)
					}
				}
				go a.fireHolmes(ctx, n, graph, budget, completionCh, trigger, intent)
			} else {
				// Tool/compute nodes pull their own context via graph.Context inside
				// the dispatcher / compute layer.
				go a.fireNode(ctx, n, graph, budget, completionCh, trigger.ID, throttle, intent, trigger.Scope)
			}
		}
	}

	// injectInterjection checks for human messages and creates a gating reflection node.
	// Returns true if an interjection was injected (caller should not launchReady yet —
	// the reflection will complete and launchReady will fire then).
	injectInterjection := func() bool {
		interject := interjectFrom(ctx)
		if interject == nil {
			return false
		}
		select {
		case msg := <-interject:
			if !budget.TrySpawnNode("", true) {
				log.Printf("[dag] no LLM budget for interjection reflection, message lost: %s", Text.TruncateLog(msg, 100))
				return false
			}
			rNode := &Node{
				Type:            NodeInterjection,
				Tag:             "operator",
				OperatorMessage: msg, // the human's injected query, shown on the trace node
			}
			rID := graph.AddNode(rNode)

			// Gate all pending nodes behind this reflection
			gated := graph.GatePending(rID)
			log.Printf("[dag] interjection node %s injected, gating %d pending nodes: %s",
				rID, gated, Text.TruncateLog(msg, 100))

			graph.SetState(rID, StateRunning)
			rNode.StartedAt = time.Now()
			inflight++
			a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: rID, Node: graph.SnapshotNode(rID)})
			// Build interjection context via ContextGate. Reflector is
			// deterministic — no Query, just node returns + worklog.
			intCtx, ictxErr := graph.Context.Get(ctx, ContextRequest{
				ReturnSources: Sources(
					NodeReturns("all"),
					Worklog(20, "all"),
				),
				MaxBudget: 24000,
			})
			if ictxErr != nil {
				log.Printf("[dag] interjection context build failed: %v", ictxErr)
				intCtx = &ContextResponse{Sources: map[string]string{}}
			}
			go a.fireInterjectionReflection(ctx, rNode, graph, budget, completionCh, trigger, msg, intCtx, intent)
			return true
		default:
			return false
		}
	}

	// injectBatchReflection creates a reflection node depending on recently completed nodes.
	injectBatchReflection := func() {
		// Only one reflection at a time — prevents double injection when
		// multiple nodes complete simultaneously and hit the batch threshold
		if reflectionInflight {
			return
		}
		// Hold reflection until any in-flight debug cycle has fully terminated.
		// Otherwise we may re-read stale failures and re-investigate a fix that's
		// already running.
		if debugGraftPending > 0 {
			log.Printf("[dag] skipping batch reflection — %d debug-grafted nodes still pending", debugGraftPending)
			return
		}
		// Hold while a Holmes or microplanner cycle is in flight. Holmes's
		// per-iteration action nodes count as tool completions and would
		// otherwise trip the batch threshold mid-investigation, spawning a
		// parallel reflector that fights the in-flight one. The reflector is
		// the sync point — it only fires after the current cycle drains.
		if debuggerInflight {
			log.Printf("[dag] skipping batch reflection — holmes/microplanner cycle in flight")
			return
		}
		// Reserve at least 1 LLM call for the aggregator
		if budget.LLMRemaining() <= 1 {
			log.Printf("[dag] skipping batch reflection — reserving budget for aggregator")
			return
		}
		if !budget.TrySpawnNode("", true) {
			log.Printf("[dag] no LLM budget for batch reflection")
			return
		}
		rNode := &Node{
			Type: NodeReflection,
			Tag:  fmt.Sprintf("batch_reflect_%d", batchCounter),
		}
		rID := graph.AddNode(rNode)
		graph.SetState(rID, StateRunning)
		rNode.StartedAt = time.Now()
		inflight++
		a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: rID, Node: graph.SnapshotNode(rID)})
		// Build batch reflection context via ContextGate. Deterministic, no curator.
		batchCtxResp, bctxErr := graph.Context.Get(ctx, ContextRequest{
			ReturnSources: Sources(
				NodeReturns("all"),
				Worklog(10, "all"),
			),
			MaxBudget: 24000,
		})
		if bctxErr != nil {
			log.Printf("[dag] batch reflection context build failed: %v", bctxErr)
			batchCtxResp = &ContextResponse{Sources: map[string]string{}}
		}
		go a.fireReflection(ctx, rNode, graph, budget, completionCh, trigger, batchCtxResp, intent)
		budget.ResetBatchCounters()
		batchCounter = 0
		log.Printf("[dag] batch reflection injected at %s", rID)
	}

	maxInvestigations := a.cfg.MaxInvestigations
	if maxInvestigations <= 0 {
		maxInvestigations = 1
	}
	investigationCount := 0
	// Replan is the graph's growth path — a batch succeeds and reveals more work
	// (expand), or a failure needs a debug step (repair). Capped so a run can't
	// grow forever; the diminishing→conclude brake below is the soft backstop.
	// The cap is the configured base (a floor) auto-scaled UP by how hard the
	// executive's initial plan looks: a bigger / compute-heavier plan gets more
	// room to expand. The plan IS the difficulty signal — no extra LLM call.
	maxReplans := a.cfg.MaxReplans
	if maxReplans <= 0 {
		maxReplans = 3
	}
	if planResult != nil {
		scaled := scaleReplanCap(maxReplans, planResult.Steps)
		if scaled != maxReplans {
			log.Printf("[dag] replan cap auto-scaled %d → %d (%d plan steps)", maxReplans, scaled, len(planResult.Steps))
		}
		maxReplans = scaled
	}
	replanCount := 0
	schedulerStart := time.Now()
	// diminishingStreak tracks consecutive reflector passes that reported
	// progress=diminishing. Two in a row downgrades the current decision
	// from "replan" to "conclude" so we don't spawn fresh debug/expand batches
	// when work isn't moving the needle.
	diminishingStreak := 0
	// debuggerInflight is declared above (alongside other top-level state)
	// so injectBatchReflection can close over it.

	launchReady()

	// Main scheduler loop. Stays alive until reflection concludes or budget/time runs out.
	// When inflight hits 0, injects a reflection instead of exiting — the reflection
	// either concludes (loop exits) or investigates (new nodes graft, loop continues).
	for !reflectionConcluded {
		// If nothing is running, inject a reflection to evaluate and decide next steps.
		if inflight == 0 && debuggerInflight {
			// Shouldn't happen — debugger is counted in inflight. Defensive
			// log only; the reflection that fires below will clear the flag
			// at its launchReady fire site.
			log.Printf("[dag] WARNING: debuggerInflight but inflight==0 (reflector will clear)")
		}
		if inflight == 0 {
			if workSinceReflection == 0 {
				// No work happened since last reflection — nothing more to evaluate
				break
			}
			if investigationCount >= maxInvestigations {
				log.Printf("[dag] max investigations (%d) reached, forcing conclude", maxInvestigations)
				break
			}
			if budget.LLMRemaining() <= 2 || ctx.Err() != nil {
				break
			}
			if !budget.TrySpawnNode("", true) {
				break
			}
			// If a debug cycle is still in flight (any of its grafted children
			// has not yet reached a terminal state), wait for it to finish
			// before reflecting. Reflecting mid-fix re-reads stale failure
			// state and triggers a duplicate investigation.
			if debugGraftPending > 0 {
				log.Printf("[dag] holding reflection — %d debug-grafted nodes still pending", debugGraftPending)
				continue
			}
			// Before reflecting, supersede any failures a debugger cycle has
			// already addressed — otherwise the reflector will keep re-investigating
			// the same resolved issue.
			if marked, fixed := graph.SupersedeFailuresIfDebugSucceeded(); marked > 0 {
				log.Printf("[dag] superseded %d failed node(s) — addressed by successful debug cycle", marked)
				// Write a FIXED marker to the worklog for each completed debug
				// cycle. The reflector reads the worklog and uses these markers
				// to recognize that a recurring symptom was already addressed,
				// avoiding the "fix it, see stale error, fix it again" loop.
				for _, dbg := range fixed {
					summary := extractDebugSummary(dbg.Result)
					if summary == "" {
						summary = fmt.Sprintf("%d-step fix applied", len(dbg.Children))
					}
					appendWorklog(a.cfg.MetadataDir, graph.SessionID, dbg.Tag, "FIXED", summary)
				}
			}
			log.Printf("[dag] injecting reflection (%d tool completions since last reflect, investigation %d/%d, replan %d/%d)", workSinceReflection, investigationCount, maxInvestigations, replanCount, maxReplans)
			budgetLine := fmt.Sprintf("replan round %d of %d, debug round %d of %d, %s elapsed.",
				replanCount, maxReplans, investigationCount, maxInvestigations, time.Since(schedulerStart).Round(time.Second))
			rNode := &Node{
				Type:   NodeReflection,
				Tag:    "reflect",
				Params: map[string]any{"investigation_count": investigationCount, "budget": budgetLine},
			}
			rID := graph.AddNode(rNode)
			graph.SetState(rID, StateRunning)
			rNode.StartedAt = time.Now()
			inflight = 1
			reflectionInflight = true
			// Sync point: previous Holmes/microplanner cycle is provably done
			// by the time we get here (inflight has dropped to 0). Clear once.
			debuggerInflight = false
			a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: rID, Node: graph.SnapshotNode(rID)})
			// Build main reflection context via ContextGate. Deterministic, no curator.
			reflCtxResp, rctxErr := graph.Context.Get(ctx, ContextRequest{
				ReturnSources: Sources(
					NodeReturns("all"),
					Worklog(10, "all"),
				),
				MaxBudget: 24000,
			})
			if rctxErr != nil {
				log.Printf("[dag] reflection context build failed: %v", rctxErr)
				reflCtxResp = &ContextResponse{Sources: map[string]string{}}
			}
			go a.fireReflection(ctx, rNode, graph, budget, completionCh, trigger, reflCtxResp, intent)
		}

		select {
		case <-ctx.Done():
			// Two different things arrive here and they are not the same event:
			// the run ran out of its own time, or whoever asked for it hung up.
			// Reporting both as the wall clock sent one investigation looking
			// for a limit the engine was not applying, so they are named apart.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				log.Printf("[dag] wall clock expired, aborting %d inflight nodes", inflight)
			} else {
				log.Printf("[dag] the caller went away, aborting %d inflight nodes", inflight)
			}
			// Leave the loop, the same way every other stopping condition does.
			// Zeroing inflight alone was not enough: the loop condition is
			// !reflectionConcluded, so the next turn saw nothing running and
			// injected a reflection — a model call on the context that had just
			// been cancelled, which fails, which leaves nothing running, which
			// injects another. The run had no way out except the caller giving
			// up on it.
			reflectionConcluded = true
			graph.SkipAllPending()
			inflight = 0
			continue

		case comp := <-completionCh:
			inflight--
			node := graph.Get(comp.NodeID)
			if node == nil {
				continue
			}
			// Store token usage on the node for frontend display
			if comp.TokensIn > 0 || comp.TokensOut > 0 {
				node.TokensIn = comp.TokensIn
				node.TokensOut = comp.TokensOut
			}
			// Decrement debug-grafted pending counter when one of those nodes
			// reaches a terminal state. Used to gate reflection injection so
			// we don't re-evaluate mid-fix.
			if debugGraftIDs[comp.NodeID] {
				delete(debugGraftIDs, comp.NodeID)
				if debugGraftPending > 0 {
					debugGraftPending--
				}
			}
			// Broadcast node completion event
			a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: node.ID, Node: graph.SnapshotNode(node.ID)})

			// ── Handle observer completions ──
			if node.Type == NodeObserver {
				// Already resolved by fireObserver; process its decision
				obs, parseErr := parseObserverOutput(comp.Result)
				if parseErr != nil {
					log.Printf("[dag] observer parse failed: %v", parseErr)
				} else {
					switch obs.Action {
					case "continue":
						// no-op
					case "inject":
						if len(obs.Steps) > 0 {
							newNodes, graftErr := planStepsToNodes(obs.Steps, graph, budget, a.registry, dagMode)
							if graftErr != nil {
								log.Printf("[dag] observer inject failed: %v", graftErr)
							} else {
								for _, nn := range newNodes {
									if nn != nil {
										graph.AddChild(comp.NodeID, nn.ID)
									}
								}
								log.Printf("[dag] observer injected %d nodes (%s)", len(newNodes), obs.Reason)
							}
						}
					case "cancel":
						cancelled := graph.CancelByTags(obs.Cancel)
						log.Printf("[dag] observer cancelled %d nodes (%s)", cancelled, obs.Reason)
					case "reflect":
						if !reflectionInflight && budget.LLMRemaining() > 1 && budget.TrySpawnNode("", true) {
							rNode := &Node{
								Type: NodeReflection,
								Tag:  "observer_reflect_" + node.Tag,
							}
							rID := graph.AddNode(rNode)
							graph.SetState(rID, StateRunning)
							rNode.StartedAt = time.Now()
							inflight++
							reflectionInflight = true
							// Sync point: any prior debug cycle is acknowledged done.
							debuggerInflight = false
							a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: rID, Node: graph.SnapshotNode(rID)})
							obsCtxResp, octxErr := graph.Context.Get(ctx, ContextRequest{
								ReturnSources: Sources(
									NodeReturns("all"),
									Worklog(10, "all"),
								),
								MaxBudget: 24000,
							})
							if octxErr != nil {
								log.Printf("[dag] observer reflection context build failed: %v", octxErr)
								obsCtxResp = &ContextResponse{Sources: map[string]string{}}
							}
							go a.fireReflection(ctx, rNode, graph, budget, completionCh, trigger, obsCtxResp, intent)
							budget.ResetBatchCounters()
							log.Printf("[dag] observer triggered reflection (%s)", obs.Reason)
						}
					}
				}
				launchReady()
				continue
			}

			if comp.Err != nil {
				errMsg := comp.Err.Error()

				// What the tool DID produce, kept before the failure is handled.
				//
				// Every path below ends in continue, and the block that stores a
				// tool's output sits after them, so without this a failed step
				// loses its own result: no payload in the trace, no evidence for
				// the reflector, nothing for a reference to read. That matters
				// more now than it did — a failure reported in the result rather
				// than as a Go error reaches here too, and its detail is the
				// whole reason it is worth reading.
				//
				// Before the SetError below, not after: SetBody resolves the
				// node and SetError fails it, so the failure has to land last.
				if comp.Body != nil {
					graph.SetBody(comp.NodeID, comp.Body)
				}

				// A step that never ran because the step it reads from failed
				// without leaving anything to read. It is not a failure of its
				// own — nothing about it was attempted — and recording it as
				// one reported a single broken step twice, in the trace and in
				// the list the aggregator is told to account for.
				//
				// Only this node, and only when it actually needed a value it
				// could not get. Its own dependents reach the same branch on
				// their own turn, so the graph settles one node at a time
				// without a prune, which the failure path below deliberately
				// does not do either.
				var blocked *blockedByDep
				if errors.As(comp.Err, &blocked) {
					log.Printf("[dag] node %s (%s) never ran: %v", comp.NodeID, node.ToolName, comp.Err)
					graph.SetBlocked(comp.NodeID, comp.Err)
					appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "BLOCKED", errMsg)
					workSinceReflection++
					a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: comp.NodeID, Node: graph.SnapshotNode(comp.NodeID)})
					launchReady()
					continue
				}

				log.Printf("[dag] node %s (%s) failed: %v", comp.NodeID, node.ToolName, comp.Err)
				// debuggerInflight is cleared by the next reflection that
				// fires (Fix 4 — reflector is the sync point), so no need
				// to clear it here on individual completion failures.

				// A reflection that FAILED still has to clear the flag. It is
				// cleared where a reflection succeeds, and this branch returns
				// before reaching there — so one failed reflection left the flag
				// set for the rest of the run, and no further reflection was
				// ever scheduled. Observer and batch spawns pile up behind it
				// for the same reason.
				if node.Type == NodeReflection || node.Type == NodeInterjection {
					reflectionInflight = false
					workSinceReflection = 0
				}

				// Credentials that do not work are a configuration problem, not
				// a transient one. Retrying every remaining node against the
				// same rejected key spends the whole budget to arrive at the
				// same place, so stop here and say what is wrong.
				// Only OUR credentials, not a site refusing a fetch.
				//
				// IsAuthFailure matches "http 403" and "forbidden", which is
				// right for a provider rejecting an API key and wrong for every
				// tool that talks to the wider world. A single 403 from a public
				// page therefore ended the whole run: this branch sets
				// reflectionConcluded, skips every pending node and zeroes
				// inflight, and the loop condition is !reflectionConcluded — so
				// the reflector never ran, nothing decided whether a refused
				// source was the end of the search, and the user was told the
				// credentials had been rejected.
				//
				// Observed live on a fetch of etherscan.io, which answers 403 to
				// datacenter addresses. Four nodes, no reflect, straight to
				// synthesis.
				//
				// A tool's failure is a fact about the world. Only a stage that
				// called a model can report that the model refused us.
				if isModelStage(node) && llm.IsAuthFailure(errMsg) {
					log.Printf("[dag] the model rejected our credentials, stopping this run: %v", comp.Err)
					reflectionConcluded = true
					reflectionOutcome = "The model rejected the credentials it was given: the API key is missing, invalid, or has no access to the configured model. Nothing further was attempted on this run."
					graph.SkipAllPending()
					inflight = 0
					continue
				}

				// A failed step is work. Without this the reflector is never
				// reached, so a plan whose only step failed gets no reflection,
				// no Holmes and no repair loop — it simply stops.
				if node.Type == NodeTool || node.Type == NodeCompute || node.Type == NodeActuator {
					workSinceReflection++
				}

				// ── Three-tier retry ──
				// One attempt: skip (nothing to fix), blind (rerun as it was), or
				// twotime (up to two cheap LLM rewrites).
				graph.SetError(comp.NodeID, comp.Err)
				if retryOnce(node, comp, retryDetail(node, errMsg)) {
					launchReady() // dependents proceed meanwhile, on a failed dep
					continue
				}

				// No retry left.
				graph.SetError(comp.NodeID, comp.Err)
				switch {
				case strings.HasPrefix(node.Tag, "verify_"), strings.HasPrefix(node.Tag, "revalidate_"):
					// A check that failed says something about what it checked,
					// not about itself, and the worklog is read by a human
					// deciding what went wrong.
					appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "VALIDATION_FAIL", errMsg)
				case strings.Contains(errMsg, "gate:"):
					appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "GATE_BLOCKED", errMsg)
				default:
					appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "FAILED", errMsg)
				}
				// Don't cascade prune — let downstream nodes attempt to run.
				// The reflector will see the failure and decide what to do.
				injectInterjection()
				launchReady()
				continue

			} else if node.Type == NodeHolmes {
				// Holmes investigation iteration completed.
				// Three possible next steps:
				//   1. conclude=true   → dispatch microplanner with the RCA
				//   2. actions present → graft all as parallel tool nodes + queue
				//                        the next Holmes iteration depending on all
				//   3. iter cap hit    → force conclude with low confidence
				graph.SetBody(comp.NodeID, parseHolmesBody(comp.Result))
				out, perr := parseHolmesOutput(comp.Result)
				prevState, _ := loadHolmesState(node)
				if perr != nil || prevState == nil {
					log.Printf("[dag] holmes parse failed for %s: %v", comp.NodeID, perr)
					// Flag is cleared by the next reflection (Fix 4).
					launchReady()
					continue
				}

				// Holmes-voice prose to the worklog so future runs can see what
				// was investigated even after the trace is gone.
				if out.Reasoning != "" {
					appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "HOLMES",
						fmt.Sprintf("[iter %d/%d] %s", prevState.Iter, prevState.MaxIter, Text.TruncateLog(out.Reasoning, 240)))
				}

				// Cycle detection: if Holmes proposed a hypothesis we already
				// tested AND has not concluded, force conclude with low confidence
				// rather than spinning forever.
				cycled := !out.Conclude && holmesSeenHypothesis(prevState, out.Hypothesis)
				if cycled {
					log.Printf("[dag] holmes cycle detected on hypothesis %q — forcing conclude", out.Hypothesis)
				}

				// Iteration cap: same forced-conclude path.
				atCap := prevState.Iter >= prevState.MaxIter

				if out.Conclude || cycled || atCap {
					// CONCLUDE PATH — Holmes is done; dispatch microplanner.
					rca := out.RCA
					if rca == nil {
						rca = &RCAReport{
							RootCause:         out.Hypothesis,
							Evidence:          []string{},
							Confidence:        "low",
							SuggestedStrategy: "Investigation hit iteration cap or cycle without converging — fix planner should treat the named hypothesis as a guess.",
						}
					}
					addressing := addressingByInvestigation[prevState.InvestigationCount]
					_, err := dispatchMicroplannerWithRCA(ctx, a, graph, budget, completionCh, trigger,
						comp.NodeID, prevState.InvestigationCount, prevState.Problem, rca, addressing, intent)
					if err != nil {
						log.Printf("[dag] holmes → microplanner dispatch failed: %v", err)
						// Flag is cleared by the next reflection (Fix 4).
					} else {
						inflight++
					}
					launchReady()
					continue
				}

				// CONTINUE PATH — graft the next action as a tool node, then
				// schedule the next Holmes iteration depending on it.

				// Reserve LLM budget for the NEXT Holmes iteration before
				// we graft anything. The first iteration was budget-checked at
				// dispatch time; iterations 2..N need their own reservation
				// here so a long investigation can't outrun the LLM budget.
				// Action tool nodes are budget-checked separately inside
				// planStepsToNodes — this check is only for the LLM call.
				if !budget.TrySpawnNode("", true) {
					log.Printf("[dag] holmes iter %d: no LLM budget for next iter, forcing conclude", prevState.Iter)
					rca := &RCAReport{
						RootCause:         out.Hypothesis,
						Evidence:          []string{},
						Confidence:        "low",
						SuggestedStrategy: "Investigation halted: LLM budget exhausted before Holmes could converge. Fix planner should treat the named hypothesis as a working guess, not a proven cause.",
					}
					addressing := addressingByInvestigation[prevState.InvestigationCount]
					if _, err := dispatchMicroplannerWithRCA(ctx, a, graph, budget, completionCh, trigger,
						comp.NodeID, prevState.InvestigationCount, prevState.Problem, rca, addressing, intent); err != nil {
						log.Printf("[dag] holmes → microplanner dispatch (budget cap) failed: %v", err)
						// Flag is cleared by the next reflection (Fix 4).
					} else {
						inflight++
					}
					launchReady()
					continue
				}

				if len(out.Actions) == 0 {
					log.Printf("[dag] holmes returned no actions and no conclude — forcing conclude")
					rca := &RCAReport{
						RootCause:         out.Hypothesis,
						Evidence:          []string{},
						Confidence:        "low",
						SuggestedStrategy: "Holmes emitted no actions and no conclusion — treat as guess.",
					}
					addressing := addressingByInvestigation[prevState.InvestigationCount]
					if _, err := dispatchMicroplannerWithRCA(ctx, a, graph, budget, completionCh, trigger,
						comp.NodeID, prevState.InvestigationCount, prevState.Problem, rca, addressing, intent); err != nil {
						log.Printf("[dag] holmes → microplanner dispatch failed: %v", err)
					} else {
						inflight++
					}
					launchReady()
					continue
				}

				// Build PlanSteps for all actions and graft as parallel nodes.
				var actionSteps []PlanStep
				for i, act := range out.Actions {
					actionSteps = append(actionSteps, PlanStep{
						Tool:   act.Tool,
						Params: act.Params,
						Tag:    fmt.Sprintf("analyse_%d_act_%d_%d", prevState.InvestigationCount, prevState.Iter, i+1),
					})
				}
				newNodes, gerr := planStepsToNodes(actionSteps, graph, budget, a.registry, dagMode)
				// Filter out nil nodes (unknown tools get dropped by planStepsToNodes).
				var actionNodes []*Node
				for _, nn := range newNodes {
					if nn != nil {
						actionNodes = append(actionNodes, nn)
					}
				}
				if gerr != nil || len(actionNodes) == 0 {
					log.Printf("[dag] holmes action graft failed (%d actions, err: %v)", len(out.Actions), gerr)
					rca := &RCAReport{
						RootCause:         out.Hypothesis,
						Evidence:          []string{},
						Confidence:        "low",
						SuggestedStrategy: "Holmes proposed unrunnable actions — fix planner should treat hypothesis as a guess.",
					}
					addressing := addressingByInvestigation[prevState.InvestigationCount]
					if _, err := dispatchMicroplannerWithRCA(ctx, a, graph, budget, completionCh, trigger,
						comp.NodeID, prevState.InvestigationCount, prevState.Problem, rca, addressing, intent); err != nil {
						log.Printf("[dag] holmes → microplanner fallback dispatch failed: %v", err)
					} else {
						inflight++
					}
					launchReady()
					continue
				}
				// Mark all as Holmes-spawned and wire into graph.
				var actionNodeIDs []string
				for _, an := range actionNodes {
					an.Source = "holmes"
					graph.AddChild(comp.NodeID, an.ID)
					a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: an.ID, Node: graph.SnapshotNode(an.ID)})
					actionNodeIDs = append(actionNodeIDs, an.ID)
				}

				// Build the HolmesTurn for THIS iteration's contribution.
				// Observations will be filled in by the next iteration when it
				// reads the action nodes' results.
				thisTurn := HolmesTurn{
					Iter:       prevState.Iter,
					Reasoning:  out.Reasoning,
					Hypothesis: out.Hypothesis,
					Actions:    out.Actions,
				}

				// Spawn the next Holmes iteration node — depends on ALL actions.
				nextNode, nerr := spawnNextHolmes(graph, prevState, thisTurn, comp.NodeID, prevState.InvestigationCount, actionNodeIDs)
				if nerr != nil {
					log.Printf("[dag] holmes next iter setup failed: %v", nerr)
					launchReady()
					continue
				}
				nextNode.DependsOn = actionNodeIDs
				a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: nextNode.ID, Node: graph.SnapshotNode(nextNode.ID)})
				log.Printf("[dag] holmes iter %d → %d actions → iter %d queued",
					prevState.Iter, len(actionNodes), prevState.Iter+1)
				launchReady()

			} else if node.Type == NodeMicroPlanner {
				// Clean-room debugger completed — parse plan and graft steps
				graph.SetBody(comp.NodeID, parseMicroPlannerBody(comp.Result))

				var mpOutput microPlannerOutput
				if err := ParseLLMJSON(comp.Result, &mpOutput); err != nil {
					log.Printf("[dag] debugger parse failed: %v", err)
				} else if len(mpOutput.Steps) > 0 {
					// Safety: a fix plan must never contain a `debug` step — that
					// would recurse (debug → microplanner → debug). Drop any.
					filtered := mpOutput.Steps[:0]
					for _, s := range mpOutput.Steps {
						if s.Tool == debugToolName {
							log.Printf("[dag] dropping `debug` step from debugger plan (no debug-in-debug)")
							continue
						}
						filtered = append(filtered, s)
					}
					mpOutput.Steps = filtered

					log.Printf("[dag] debugger diagnosis: %s (%d steps)", Text.TruncateLog(mpOutput.Summary, 200), len(mpOutput.Steps))
					appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "DEBUG_PLAN", fmt.Sprintf("%d steps: %s", len(mpOutput.Steps), Text.TruncateLog(mpOutput.Summary, 150)))

					newNodes, graftErr := planStepsToNodes(mpOutput.Steps, graph, budget, a.registry, dagMode)
					if graftErr != nil {
						log.Printf("[dag] debugger graft failed: %v", graftErr)
					} else {
						// Reset the debug pending tracker for this new cycle.
						debugGraftIDs = make(map[string]bool)
						debugGraftPending = 0
						fixIDs := make([]string, 0, len(newNodes))
						for _, nn := range newNodes {
							if nn != nil {
								graph.AddChild(comp.NodeID, nn.ID)
								fixIDs = append(fixIDs, nn.ID)
								debugGraftIDs[nn.ID] = true
								debugGraftPending++
								a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: nn.ID, Node: graph.SnapshotNode(nn.ID)})
							}
						}
						// Inject blueprint_ref into compute nodes that don't have one.
						// The debugger doesn't know the file path — we do. Both the
						// deep (architect) and shallow (coder) paths use it as input
						// context; it is NOT a routing signal.
						//
						// Direct call to latestBlueprintPath (NOT via ContextGate) is
						// intentional: we need the path STRING to inject as a node
						// param, not the blueprint CONTENT. ContextGate is for
						// content retrieval; path resolution is a metadata operation
						// outside the gate's scope.
						if bpPath := latestBlueprintPath(a.cfg.MetadataDir, graph.SessionID); bpPath != "" {
							for _, nn := range newNodes {
								if nn != nil && nn.Type == NodeCompute {
									if ref, _ := nn.Params["blueprint_ref"].(string); ref == "" {
										nn.Params["blueprint_ref"] = bpPath
									}
								}
							}
						}
						log.Printf("[dag] debugger grafted %d nodes", len(fixIDs))

						// Re-graft stored validators after debug plan
						if len(fixIDs) > 0 && len(graph.Validators) > 0 {
							rv := 0
							for _, v := range graph.Validators {
								if !budget.TrySpawnNode("bash", false) {
									break
								}
								vNode := &Node{
									Type:      NodeTool,
									ToolName:  "bash",
									Params:    map[string]any{"command": "sleep 3 && " + v.Check, "timeout_sec": 20},
									DependsOn: append([]string{}, fixIDs...),
									SpawnedBy: comp.NodeID,
									Tag:       "revalidate_" + sanitizeTag(v.Name),
									Source:    "builtin",
								}
								vID := graph.AddNode(vNode)
								graph.AddChild(comp.NodeID, vID)
								debugGraftIDs[vID] = true
								debugGraftPending++
								a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: vID, Node: graph.SnapshotNode(vID)})
								rv++
							}
							if rv > 0 {
								log.Printf("[dag] re-grafted %d validators after debug plan", rv)
							}
						}
					}
				} else {
					log.Printf("[dag] debugger returned no steps: %s", Text.TruncateLog(mpOutput.Summary, 200))
				}
				// debuggerInflight is cleared by the next reflection that fires
				// (Fix 4 — reflector is the unconditional sync point).
				launchReady()

			} else if node.Type == NodeReflection || node.Type == NodeInterjection {
				reflectionInflight = false
				workSinceReflection = 0
				ref, parseErr := parseReflectionOutput(comp.Result)
				if parseErr != nil {
					log.Printf("[dag] reflection parse failed, continuing: %v", parseErr)
					graph.SetResult(comp.NodeID, comp.Result)
				} else {
					// Progress classification brake — only "diminishing" has
					// scheduler-visible effect. Two consecutive diminishing
					// batches downgrade investigate/replan→conclude so Holmes
					// cycles stop spawning and the graph stops expanding when
					// work isn't moving the needle. Empty / unknown /
					// "productive" resets the streak.
					switch ref.Progress {
					case "diminishing":
						diminishingStreak++
						log.Printf("[reflector:diminishing] streak=%d (decision=%q): %s", diminishingStreak, ref.Decision, Text.TruncateLog(ref.Summary, 160))
						appendWorklog(a.cfg.MetadataDir, graph.SessionID, "reflect", "DIMINISHING", fmt.Sprintf("streak=%d | %s", diminishingStreak, Text.TruncateLog(ref.Summary, 180)))
						if diminishingStreak >= 2 && ref.Decision == "replan" {
							log.Printf("[reflector:diminishing] streak hit 2 — downgrading %s→conclude", ref.Decision)
							ref.Decision = "conclude"
							if ref.Aggregate == nil {
								t := true
								ref.Aggregate = &t
							}
							if ref.Outcome == "" {
								ref.Outcome = ref.Summary
							}
						}
					default:
						diminishingStreak = 0
					}

					switch ref.Decision {
					case "continue":
						graph.SetBody(comp.NodeID, newReflectionBody(*ref, comp.Result))
						budget.ResetBatchCounters()
						investigationCount = 0 // reset — if previous investigation worked, next reflection starts fresh
						log.Printf("[dag] reflection: continue (%s), batch counters reset", ref.Reason)
						appendWorklog(a.cfg.MetadataDir, graph.SessionID, "reflect", "CONTINUE", Text.TruncateLog(ref.Reason, 200))
						launchReady()
						// If nothing launched, the reflector expected pending steps that
						// don't exist (deduped or already completed). Force another
						// reflection so it sees "0 pending" and either investigates or concludes.
						if inflight == 0 {
							log.Printf("[dag] reflection said continue but nothing to launch — forcing re-evaluation")
							appendWorklog(a.cfg.MetadataDir, graph.SessionID, "reflect", "CONTINUE_EMPTY", "reflector expected pending steps but none remain")
							workSinceReflection = 1 // prevent the "no work" break
						}

					case "replan":
						// EXPAND: a batch succeeded and revealed the next move. Re-invoke
						// the executive to plan the next steps, graft them, keep looping.
						// This is the growth path that mirrors investigate's REPAIR path —
						// but with a diagnosis of SUCCESS ("here's what to do next") rather
						// than failure. The failure pipeline is untouched.
						graph.SetBody(comp.NodeID, newReflectionBody(*ref, comp.Result))
						budget.ResetBatchCounters()

						next := ref.Next
						if next == "" {
							next = ref.Summary
						}

						// concludeReplan collapses the run to a outcome when replan can't
						// or shouldn't continue (cap hit, no budget, executive error, no
						// new steps). Named what's missing rather than expanding further.
						concludeReplan := func(reason, outcome string) {
							if outcome == "" {
								outcome = ref.Outcome
							}
							if outcome == "" {
								outcome = ref.Summary
							}
							log.Printf("[dag] replan → conclude (%s)", reason)
							appendWorklog(a.cfg.MetadataDir, graph.SessionID, "reflect", "REPLAN_STOP", fmt.Sprintf("%s | %s", reason, Text.TruncateLog(outcome, 180)))
							graph.SetResult(comp.NodeID, outcome)
							graph.SkipAllPending()
							reflectionConcluded = true
							reflectionOutcome = outcome
							if ref.Aggregate != nil {
								reflectionAggregate = ref.Aggregate
							} else {
								t := true
								reflectionAggregate = &t
							}
						}

						// Hard cap: the graph can't expand forever.
						if replanCount >= maxReplans {
							concludeReplan(fmt.Sprintf("replan cap %d reached", maxReplans), "")
							break
						}
						if budget.LLMRemaining() <= 2 || !budget.TrySpawnNode("", true) {
							concludeReplan("no LLM budget for replan", "")
							break
						}
						replanCount++
						// Everything planned from here belongs to the new round,
						// so what this round did can be told apart from what
						// earlier ones did when the run is described.
						graph.BeginRound()
						log.Printf("[dag] reflection: replan #%d/%d — %s", replanCount, maxReplans, Text.TruncateLog(next, 200))
						appendWorklog(a.cfg.MetadataDir, graph.SessionID, "reflect", "REPLAN", fmt.Sprintf("#%d/%d | %s", replanCount, maxReplans, Text.TruncateLog(next, 200)))
						// Record this round for the reflector's ## History so the NEXT
						// reflection can see what was already tried and not loop on it.
						graph.AddReplanRecord(fmt.Sprintf("Round %d — %s (tried next: %s)", replanCount, Text.TruncateLog(ref.Summary, 180), Text.TruncateLog(next, 120)))

						// Anchor the user's goal verbatim (formatTrigger inside the
						// executive); hand it a generic frame: what's already done
						// (worklog) + the reflector's `next`. The executive decides HOW.
						frame := fmt.Sprintf(replanFrameTemplate, next)
						// How to diagnose a failure, only where there is one.
						// FailedNodes already excludes the ones a debug cycle
						// has since addressed, so a run whose failures were
						// fixed stops being told about them.
						if len(graph.FailedNodes()) > 0 {
							frame += replanDebugParagraph
						}

						// Its own id, not "executive".
						//
						// Every planning round used to broadcast under the same
						// one, and the frontend keys nodes by id — so a re-plan
						// overwrote the row the first plan had written. The
						// tools that plan was shown went with it, the row read
						// "loading" for as long as the re-plan ran, and the
						// original reappeared at the end when the final
						// snapshot was merged. Three rounds of planning are
						// three things that happened; they are three rows.
						planID := fmt.Sprintf("executive-r%d", replanCount)
						a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: planID, Node: &NodeInfo{ID: planID, Type: "executive", State: "running", Tag: fmt.Sprintf("replan %d", replanCount)}})
						replanResult, rerr := a.runExecutive(ctx, trigger, graph, frame)
						if rerr != nil {
							// Executive answered directly (no tools needed) → that answer IS the outcome.
							var convErr *ExecutiveConversationalError
							if errors.As(rerr, &convErr) {
								a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: planID, Node: &NodeInfo{ID: planID, Type: "executive", State: "resolved", Tag: "replan direct"}})
								concludeReplan("executive answered directly", convErr.Text)
								break
							}
							// The planner had nothing to add and the reflector had
							// asked for more. They disagree, and the run is done —
							// which is an ending, not a failure. Shown as a failed
							// step it read as a broken run to anyone looking at the
							// trace, for something that simply stopped.
							var noMove *ExecutiveNoMove
							if errors.As(rerr, &noMove) {
								a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: planID, Node: &NodeInfo{
									ID: planID, Type: "executive", State: "resolved", Tag: "no further steps",
									Summary: "the planner had no move the reflector's request could be met with"}})
								// Its answer is NOT the outcome. The planner reached
								// for one instead of planning, and it is recalled
								// rather than computed — the reflector's account of
								// what actually ran is what the user gets. See
								// TestAnEmptyRePlanIsRefused.
								concludeReplan("the planner had no further step to add", "")
								break
							}
							a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: planID, Node: &NodeInfo{ID: planID, Type: "executive", State: "failed", Tag: fmt.Sprintf("replan %d", replanCount), Error: rerr.Error()}})
							concludeReplan(fmt.Sprintf("replan executive failed: %v", rerr), "")
							break
						}
						a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: planID, Node: &NodeInfo{
							ID: planID, Type: "executive", State: "resolved", Tag: fmt.Sprintf("replan %d", replanCount),
							Tools: replanResult.Tools, Objective: replanResult.Objective}})

						newNodes, gerr := planStepsToNodes(replanResult.Steps, graph, budget, a.registry, dagMode)
						if gerr != nil {
							concludeReplan(fmt.Sprintf("replan graft failed: %v", gerr), "")
							break
						}
						grafted := 0
						for _, nn := range newNodes {
							if nn != nil {
								nn.SpawnedBy = comp.NodeID
								graph.AddChild(comp.NodeID, nn.ID)
								grafted++
							}
						}
						if grafted == 0 {
							// Nothing new to run (all steps deduped/dropped) — the goal
							// is as answered as it's going to get. Conclude on it.
							concludeReplan("executive returned no new steps", "")
							break
						}
						log.Printf("[dag] replan #%d grafted %d new node(s)", replanCount, grafted)
						workSinceReflection = 1 // ensure the next quiescence reflects instead of breaking
						launchReady()

					case "conclude":
						// Store the whole reflection (not just the outcome) so
						// Decision/Next/Summary/Aggregate survive on the node. The
						// outcome still surfaces via reflectionOutcome below.
						graph.SetBody(comp.NodeID, newReflectionBody(*ref, comp.Result))
						graph.SkipAllPending()
						reflectionConcluded = true
						reflectionOutcome = ref.Outcome
						reflectionAggregate = ref.Aggregate
						log.Printf("[dag] reflection: conclude early (%s)", ref.Reason)
						appendWorklog(a.cfg.MetadataDir, graph.SessionID, "reflect", "CONCLUDE", Text.TruncateLog(ref.Reason, 200))

						// NOTE: `investigate` is gone as a reflection decision. Repair
						// now flows through the SAME door as expand: the reflector
						// emits `replan`, the executive plans a `debug` super-tool
						// step, and the Holmes investigation is grafted when that
						// debug node completes (see the debug graft in the
						// tool-completion branch below). parseReflectionOutput
						// coerces any stray "investigate" → "replan" for safety.
					}
				}

			} else {
				// Tool/compute node resolved successfully
				if comp.Body != nil {
					graph.SetBody(comp.NodeID, comp.Body)
				} else if msg, ok := toolapi.ParseToolMessage(comp.Result); ok {
					graph.SetBody(comp.NodeID, toolMessageBody{msg: msg})
				} else if node.Type == NodeTool {
					// A tool that declared no outcome — prose, its own JSON shape,
					// a plugin. It used to arrive as a bare string, which every
					// consumer had to recognise by the body's Go type, and most
					// read as "nothing to report here". Saying so costs nothing
					// and stops absence being mistaken for success.
					graph.SetBody(comp.NodeID, toolMessageBody{msg: toolapi.ToolUnclassified(comp.Result)})
				} else {
					graph.SetResult(comp.NodeID, comp.Result)
				}

				if node.Type == NodeTool || node.Type == NodeCompute || node.Type == NodeActuator {
					workSinceReflection++
					if strings.HasPrefix(node.Tag, "verify_") || strings.HasPrefix(node.Tag, "revalidate_") {
						// Catch false positives: bash exited 0 but output indicates failure.
						// LLM classifier is authoritative; falls back to heuristic on error.
						if failed, reason := a.validatorFailed(ctx, node.Tag, comp.Result); failed {
							log.Printf("[dag] node %s validator false positive (%s): %s", comp.NodeID, reason, Text.TruncateLog(comp.Result, 200))
							fakeErr := fmt.Errorf("validator output indicates failure (%s): %s", reason, Text.TruncateLog(comp.Result, 200))
							graph.SetError(comp.NodeID, fakeErr)
							node.Error = fakeErr
							appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "VALIDATION_FAIL", comp.Result)
							// Prune downstream siblings — this validator proved the
							// fix didn't work, so service restarts and other nodes
							// that were going to run AFTER this verify should not
							// fire on broken code. The reflector still sees the
							// VALIDATION_FAIL and opens a fresh debug cycle.
							graph.PruneBranch(comp.NodeID)
							launchReady()
							continue
						}
						appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "VALIDATION_PASS", Text.TruncateLog(comp.Result, 100))
					} else {
						// The evidence, not comp.Result. For a typed tool comp.Result is the
						// serialised envelope, so 44 of these 100 characters went to
						// {"type":…,"status":…,"content":" before any output — and a 74-byte
						// result came out stamped "..." as though it had been cut. The
						// reframe then read that marker and told the next stage the data
						// was incomplete.
						appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "OK", fmt.Sprintf("%s: %s", node.ToolName, Text.TruncateLog(nodeEvidence(node, comp), 100)))
					}
				}
				log.Printf("[dag] node %s (%s) resolved (%d bytes): %s",
					comp.NodeID, node.ToolName, len(comp.Result), Text.TruncateLog(comp.Result, 500))

				// ── Expose exec stdout on the parent compute's result ──
				// When a shallow-compute node's auto-grafted exec child
				// finishes, merge its captured stdout into the compute parent's
				// Result under an "output" field. Without this, downstream
				// planner steps that param_ref the compute node have no way to
				// reach the script's printed output — compute's raw result only
				// describes the emitted code (code_path, execute, etc.), not
				// what the code produced when run. One field keeps compute
				// chainable without the planner needing to know about the
				// scheduler-internal exec node.
				if node.ToolName == "bash" && node.SpawnedBy != "" && strings.HasPrefix(node.Tag, "exec_") {
					parent := graph.Get(node.SpawnedBy)
					if parent != nil && parent.Type == NodeCompute && parent.Result != "" {
						stdout := extractBashStdout(comp.Result)
						if stdout != "" {
							// The output goes inside compute's own JSON, not
							// beside the envelope's keys, so ${node.X.output}
							// resolves where every other compute field is.
							merged, err := mergeJSONField(computePayload(parent.Result), "output", stdout)
							if err == nil {
								parent.Result = withComputePayload(parent.Result, merged)
								parent.Body = NewToolBody(computeMessage("compute", merged)) // keep the body in step with the spliced payload
								log.Printf("[dag] exposed %d bytes of exec stdout on compute parent %s as .output", len(stdout), parent.ID)
							}
						}
					}
				}

				// ── Auto-graft health check after service start ──
				// When a service starts, graft a delayed curl check so the
				// reflector has real evidence of whether it's actually listening.
				if node.ToolName == "service" {
					var svcResult struct {
						Status string `json:"status"`
						Name   string `json:"name"`
						PID    int    `json:"pid"`
						Port   int    `json:"port"`
					}
					// service now emits a ToolMessage envelope; the action payload
					// (status/name/pid/port) is in Data.
					svcSrc := comp.Result
					if msg, ok := toolapi.ParseToolMessage(comp.Result); ok {
						svcSrc = string(msg.Data)
					}
					if json.Unmarshal([]byte(svcSrc), &svcResult) == nil && svcResult.Status == "started" {
						// Determine port: prefer explicit port from service result,
						// then try to extract from the original service command,
						// then fall back to name-based heuristic.
						port := ""
						if svcResult.Port > 0 {
							port = fmt.Sprintf("%d", svcResult.Port)
						}
						if port == "" {
							// Try to extract port from the node's params (planner may have set it)
							if p, ok := node.Params["port"].(float64); ok && p > 0 {
								port = fmt.Sprintf("%d", int(p))
							}
						}
						if port == "" {
							// Heuristic from service name
							if strings.Contains(svcResult.Name, "backend") || strings.Contains(svcResult.Name, "api") {
								port = "4000"
							} else {
								port = "3000"
							}
						}
						// Health check: wait for service to initialize, then retry curl
						// with backoff. On failure, dump the error log for diagnosis.
						checkCmd := fmt.Sprintf(
							"for i in 1 2 3; do sleep 5; BODY=$(curl -sf http://localhost:%s/ 2>/dev/null || curl -sf http://localhost:%s/health 2>/dev/null); if [ -n \"$BODY\" ]; then echo \"$BODY\" | head -5; exit 0; fi; done; echo '--- SERVICE ERROR LOG ---' && cat .services/%s.err.log 2>/dev/null | tail -30 && exit 1",
							port, port, svcResult.Name)
						if budget.TrySpawnNode("bash", false) {
							healthNode := &Node{
								Type:      NodeTool,
								ToolName:  "bash",
								Params:    map[string]any{"command": checkCmd, "timeout_sec": 30},
								DependsOn: []string{comp.NodeID},
								SpawnedBy: comp.NodeID,
								Tag:       "verify_" + svcResult.Name + "_health",
								Source:    "builtin",
							}
							hID := graph.AddNode(healthNode)
							graph.AddChild(comp.NodeID, hID)
							log.Printf("[dag] auto-grafted health check %s for service %s (port %s)", hID, svcResult.Name, port)
							appendWorklog(a.cfg.MetadataDir, graph.SessionID, svcResult.Name, "SERVICE_START", fmt.Sprintf("pid %d, health check grafted on port %s", svcResult.PID, port))
							a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: hID, Node: graph.SnapshotNode(hID)})
						}
					}
				}

				// ── Debug super-tool graft ──
				// A resolved `debug` tool node is the executive-planned trigger
				// for a Holmes investigation. Its result is a {type:"debug",
				// problem} envelope. Graft the first Holmes iteration here (in
				// the main loop — no race), parented to the debug node; the
				// existing NodeHolmes → microplanner → validator handlers drive
				// the fix to completion, fully visible in the DAG. This is the
				// REPAIR path flowing through the SAME door as expand:
				// reflect.replan → executive plans `debug` → this graft. We do
				// NOT SkipAllPending — debug is a planned leaf, and independent
				// sibling work should keep running.
				if node.Type == NodeTool && node.ToolName == debugToolName {
					if problem, isDebug := debugProblem(comp); isDebug {
						if problem == "" {
							// Synthesize a brief from the current failures.
							var fails []string
							for _, fn := range graph.FailedNodes() {
								if fn.Error != nil {
									fails = append(fails, fmt.Sprintf("%s: %s", fn.Tag, fn.Error.Error()))
								}
							}
							problem = "a step failed; diagnose the root cause. " + strings.Join(fails, " | ")
						}
						if budget.TrySpawnNode("", true) {
							investigationCount++
							// Snapshot currently-failed node IDs so they can be
							// marked superseded once the fix succeeds — same
							// machinery the old investigate branch used.
							var addressing []string
							for _, fn := range graph.FailedNodes() {
								addressing = append(addressing, fn.ID)
							}
							addressingByInvestigation[investigationCount] = addressing

							maxIter := a.cfg.MaxHolmesIters
							if maxIter <= 0 {
								maxIter = 5
							}
							sNode, err := spawnFirstHolmes(graph, problem, comp.NodeID, investigationCount, maxIter)
							if err != nil {
								log.Printf("[dag] holmes setup failed (debug node %s): %v", comp.NodeID, err)
							} else {
								inflight++
								a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: sNode.ID, Node: graph.SnapshotNode(sNode.ID)})
								go a.fireHolmes(ctx, sNode, graph, budget, completionCh, trigger, intent)
								debuggerInflight = true
								log.Printf("[dag] debug node %s dispatched holmes %s (iter 1/%d): %s", comp.NodeID, sNode.ID, maxIter, Text.TruncateLog(problem, 200))
							}
						} else {
							log.Printf("[dag] no budget for holmes from debug node %s", comp.NodeID)
						}
					}
				}

				// ── Compute plan follow-up graft ──
				if node.Type == NodeCompute {
					var cr struct {
						Type        string          `json:"type"`
						ProjectRoot string          `json:"project_root,omitempty"`
						Setup       []string        `json:"setup,omitempty"`
						FollowUp    json.RawMessage `json:"follow_up,omitempty"`
						Execute     string          `json:"execute,omitempty"`
						Services    []struct {
							Name    string `json:"name"`
							Command string `json:"command"`
							Workdir string `json:"workdir,omitempty"`
							Port    int    `json:"port,omitempty"`
						} `json:"services,omitempty"`
						Validation []struct {
							Name   string `json:"name"`
							Check  string `json:"check"`
							Expect string `json:"expect"`
						} `json:"validation,omitempty"`
					}
					unmarshalErr := json.Unmarshal([]byte(computePayload(comp.Result)), &cr)
					log.Printf("[dag] compute plan post-parse: err=%v type=%q followup_bytes=%d services=%d validation=%d",
						unmarshalErr, cr.Type, len(cr.FollowUp), len(cr.Services), len(cr.Validation))
					if unmarshalErr == nil && cr.Type == "blueprint" && len(cr.FollowUp) > 0 {
						// Store the project root on the graph so all downstream
						// components (coders, services, Holmes) can find it.
						if cr.ProjectRoot != "" && graph.ProjectRoot == "" {
							graph.ProjectRoot = cr.ProjectRoot
							log.Printf("[dag] project root set: %s", graph.ProjectRoot)
						}
						// Parse follow_up as array of work items
						var followUps []struct {
							Tool           string         `json:"tool"`
							Tag            string         `json:"tag"`
							Params         map[string]any `json:"params"`
							DependsOnTasks []int          `json:"depends_on_tasks"`
						}
						if err := json.Unmarshal(cr.FollowUp, &followUps); err != nil {
							// Fallback: try parsing as single object (backward compat)
							var single struct {
								Tool   string         `json:"tool"`
								Params map[string]any `json:"params"`
							}
							if json.Unmarshal(cr.FollowUp, &single) == nil && single.Tool != "" {
								followUps = append(followUps, struct {
									Tool           string         `json:"tool"`
									Tag            string         `json:"tag"`
									Params         map[string]any `json:"params"`
									DependsOnTasks []int          `json:"depends_on_tasks"`
								}{Tool: single.Tool, Tag: node.Tag + "_code", Params: single.Params})
							}
						}

						var allGraftedNodes []*Node

						// Phase 1: Graft sequential bash setup nodes
						lastDepID := comp.NodeID
						for si, cmd := range cr.Setup {
							if !budget.TrySpawnNode("bash", false) {
								break
							}
							setupNode := &Node{
								Type:      NodeTool,
								ToolName:  "bash",
								Params:    map[string]any{"command": cmd},
								DependsOn: []string{lastDepID},
								SpawnedBy: comp.NodeID,
								Tag:       fmt.Sprintf("setup_%d", si),
								Source:    "builtin",
							}
							sID := graph.AddNode(setupNode)
							lastDepID = sID
							allGraftedNodes = append(allGraftedNodes, setupNode)
							graph.AddChild(comp.NodeID, sID)
							a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: sID, Node: graph.SnapshotNode(sID)})
						}
						if len(cr.Setup) > 0 {
							log.Printf("[dag] compute plan → grafted %d setup bash nodes", len(cr.Setup))
						}

						// Phase 2: Graft compute nodes for each task (depend on last setup node)
						computeNodes := make([]*Node, len(followUps))
						computeIDs := make([]string, len(followUps))

						for i, fu := range followUps {
							// Always graft all architect tasks — never cut.
							// Partial builds are worse than no build.
							budget.TrySpawnNode("compute", true) // charge budget but don't block
							fuTag := fu.Tag
							if fuTag == "" {
								fuTag = fmt.Sprintf("%s_%d", node.Tag, i)
							}
							followNode := &Node{
								Type:      NodeCompute,
								ToolName:  fu.Tool,
								Params:    fu.Params,
								DependsOn: []string{lastDepID},
								SpawnedBy: comp.NodeID,
								Tag:       fuTag,
								Source:    node.Source,
							}
							fID := graph.AddNode(followNode)
							computeIDs[i] = fID
							computeNodes[i] = followNode
							allGraftedNodes = append(allGraftedNodes, followNode)
							graph.AddChild(comp.NodeID, fID)
							a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: fID, Node: graph.SnapshotNode(fID)})
						}

						// Resolve inter-task dependencies
						for i, fu := range followUps {
							if computeNodes[i] == nil {
								continue
							}
							for _, depIdx := range fu.DependsOnTasks {
								if depIdx >= 0 && depIdx < len(computeIDs) && computeIDs[depIdx] != "" {
									computeNodes[i].DependsOn = append(computeNodes[i].DependsOn, computeIDs[depIdx])
								}
							}
						}

						// Phase 3: Graft execute/service nodes from the architect's
						// task params. Each depends on ALL coders completing (not
						// just its own) because servers typically import files
						// produced by sibling coders.
						allCoderIDs := make([]string, 0, len(computeIDs))
						for _, cid := range computeIDs {
							if cid != "" {
								allCoderIDs = append(allCoderIDs, cid)
							}
						}
						for i, fu := range followUps {
							// One-shot execute — only for grafted coders
							if computeIDs[i] != "" {
								if execCmd, ok := fu.Params["execute"].(string); ok && execCmd != "" {
									svcCmd := ""
									if svc, ok := fu.Params["service"].(map[string]any); ok {
										svcCmd, _ = svc["command"].(string)
									}
									if execCmd == svcCmd {
										log.Printf("[dag] skipping execute node for %s — same command declared as service", computeNodes[i].Tag)
									} else if budget.TrySpawnNode("bash", false) {
										// This is the step ComputeTimeout is about: the
										// code compute just wrote, being run. It set no
										// timeout, so bash's own 60s applied and an
										// operator raising tools.compute.timeout_sec got
										// no change and no warning — both applications
										// default that setting to 120.
										//
										// Left unset when the setting is, so bash's
										// default still applies rather than a zero being
										// passed as "no time at all".
										execParams := map[string]any{"command": execCmd}
										if secs := int(a.cfg.ComputeTimeout.Seconds()); secs > 0 {
											execParams["timeout_sec"] = secs
										}
										execNode := &Node{
											Type:      NodeTool,
											ToolName:  "bash",
											Params:    execParams,
											DependsOn: append([]string{}, allCoderIDs...),
											SpawnedBy: comp.NodeID,
											Tag:       computeNodes[i].Tag + "_exec",
											Source:    "builtin",
										}
										eID := graph.AddNode(execNode)
										allGraftedNodes = append(allGraftedNodes, execNode)
										graph.AddChild(comp.NodeID, eID)
										a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: eID, Node: graph.SnapshotNode(eID)})
										log.Printf("[dag] compute plan → grafted execute node %s: %s", eID, execCmd)
									}
								}
							} // end computeIDs[i] != "" (execute only for grafted coders)
							// Long-running service — ALWAYS graft, even if coder was budget-cut.
							// Services are infrastructure, not per-coder tasks.
							if svc, ok := fu.Params["service"].(map[string]any); ok {
								svcCmd, _ := svc["command"].(string)
								svcName, _ := svc["name"].(string)
								svcWorkdir, _ := svc["workdir"].(string)
								svcPort := 0
								if p, ok := svc["port"].(float64); ok {
									svcPort = int(p)
								}
								if svcCmd != "" {
									if svcName == "" {
										svcName = computeNodes[i].Tag + "_svc"
									}
									if budget.TrySpawnNode("service", false) {
										svcParams := map[string]any{"action": "start", "command": svcCmd, "name": svcName}
										if svcWorkdir != "" {
											svcParams["workdir"] = svcWorkdir
										}
										if svcPort > 0 {
											svcParams["port"] = float64(svcPort)
										}
										svcNode := &Node{
											Type:      NodeTool,
											ToolName:  "service",
											Params:    svcParams,
											DependsOn: append([]string{}, allCoderIDs...),
											SpawnedBy: comp.NodeID,
											Tag:       svcName,
											Source:    "builtin",
										}
										sID := graph.AddNode(svcNode)
										allGraftedNodes = append(allGraftedNodes, svcNode)
										graph.AddChild(comp.NodeID, sID)
										a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: sID, Node: graph.SnapshotNode(sID)})
										log.Printf("[dag] compute plan → grafted service node %s: %s", sID, svcCmd)
									}
								}
							}
						}

						// Phase 3.5: top-level services — architect-declared services
						// that aren't tied to any specific task. These are always grafted
						// (no budget gate) because services are infrastructure.
						log.Printf("[dag] compute plan → phase 3.5: %d top-level services to graft", len(cr.Services))
						for _, svc := range cr.Services {
							if svc.Command == "" {
								continue
							}
							name := svc.Name
							if name == "" {
								name = "service"
							}
							// Skip if a per-task service with the same name was already grafted.
							alreadyGrafted := false
							for _, gn := range allGraftedNodes {
								if gn.ToolName == "service" && gn.Tag == name {
									alreadyGrafted = true
									break
								}
							}
							if alreadyGrafted {
								log.Printf("[dag] skipping top-level service %s — already grafted from task", name)
								continue
							}
							if !budget.TrySpawnNode("service", false) {
								log.Printf("[dag] budget exhausted, skipping remaining top-level services")
								break
							}
							svcParams := map[string]any{"action": "start", "command": svc.Command, "name": name}
							if svc.Workdir != "" {
								svcParams["workdir"] = svc.Workdir
							}
							if svc.Port > 0 {
								svcParams["port"] = float64(svc.Port)
							}
							svcNode := &Node{
								Type:      NodeTool,
								ToolName:  "service",
								Params:    svcParams,
								DependsOn: append([]string{}, allCoderIDs...),
								SpawnedBy: comp.NodeID,
								Tag:       name,
								Source:    "builtin",
							}
							sID := graph.AddNode(svcNode)
							allGraftedNodes = append(allGraftedNodes, svcNode)
							graph.AddChild(comp.NodeID, sID)
							a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: sID, Node: graph.SnapshotNode(sID)})
							log.Printf("[dag] compute plan → grafted top-level service node %s: %s", sID, svc.Command)
						}

						// Phase 4: validation batch — architect-declared checks.
						// Each validation entry becomes a bash node running its
						// check command, depending on all Phase 1-3 grafted
						// nodes so it runs only after setup + coders complete.
						// Reflector sees pass/fail as structured evidence of
						// goal achievement.
						if len(cr.Validation) > 0 {
							// Store validators on the graph for replay after investigations.
							for _, v := range cr.Validation {
								if v.Check != "" {
									graph.Validators = append(graph.Validators, ValidatorDef{
										Name:  v.Name,
										Check: v.Check,
									})
								}
							}

							var priorIDs []string
							for _, gn := range allGraftedNodes {
								priorIDs = append(priorIDs, gn.ID)
							}
							var validationNodes []*Node
							for _, v := range cr.Validation {
								if !budget.TrySpawnNode("bash", false) {
									log.Printf("[dag] budget exhausted, skipping remaining validation checks")
									break
								}
								if v.Check == "" {
									continue
								}
								verifyTag := "verify_" + sanitizeTag(v.Name)
								if verifyTag == "verify_" {
									verifyTag = fmt.Sprintf("verify_%d", len(validationNodes))
								}
								verifyNode := &Node{
									Type:      NodeTool,
									ToolName:  "bash",
									Params:    map[string]any{"command": "sleep 3 && " + v.Check, "timeout_sec": 20},
									DependsOn: append([]string{}, priorIDs...),
									SpawnedBy: comp.NodeID,
									Tag:       verifyTag,
									Source:    "builtin",
								}
								vID := graph.AddNode(verifyNode)
								graph.AddChild(comp.NodeID, vID)
								validationNodes = append(validationNodes, verifyNode)
								allGraftedNodes = append(allGraftedNodes, verifyNode)
								a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: vID, Node: graph.SnapshotNode(vID)})
							}
							if len(validationNodes) > 0 {
								log.Printf("[dag] compute plan → grafted %d validation checks", len(validationNodes))
							}
						} else {
							log.Printf("[dag] compute plan emitted no validation — no validation batch grafted")
						}

						// Rewrite downstream nodes to depend on all grafted nodes
						if len(allGraftedNodes) > 0 {
							rewriteDependentsMultiExcluding(graph, comp.NodeID, allGraftedNodes)
							log.Printf("[dag] compute plan → grafted %d tasks", len(followUps))
						}
					}

					// NOTE: Phase 4 coder-result execute/service grafting was removed
					// (2026-04-07) for architect-spawned coders — the architect is
					// the sole authority there. But for TOP-LEVEL (shallow-mode)
					// compute nodes the planner called directly, there is no
					// architect: without an execute+validate graft, generated code
					// is written and never run, and the reflector sees only
					// metadata and concludes "done" on an empty answer.
					//
					// Guarded by SpawnedBy == "" so we only graft for planner-
					// spawned top-level compute — architect children still route
					// through Phase 3 above.
					if node.SpawnedBy == "" {
						var res struct {
							Execute string `json:"execute,omitempty"`
							Type    string `json:"type,omitempty"`
						}
						if json.Unmarshal([]byte(computePayload(comp.Result)), &res) == nil && res.Type == "result" && res.Execute != "" {
							grafted := a.graftComputeExecution(graph, node, comp.NodeID, res.Execute, budget)
							if len(grafted) > 0 {
								rewriteDependentsMultiExcluding(graph, comp.NodeID, grafted)
								log.Printf("[dag] shallow compute → grafted %d exec nodes", len(grafted))
							}
						}
					}
				}

				// ── Mode-specific post-completion logic ──
				switch dagMode {
				case DAGModeOrchestrator:
					// Spawn an orchestrator to evaluate this result
					if (node.Type == NodeTool || node.Type == NodeCompute) && budget.TryObserverCall() {
						inflight++
						go a.fireObserver(ctx, node, graph, budget, completionCh, trigger, intent)
					}

				case DAGModeNReflect:
					// Track completions and inject reflection at batch threshold
					if node.Type == NodeTool || node.Type == NodeCompute {
						batchCounter++
						if batchCounter >= a.cfg.BatchSize {
							injectBatchReflection()
						}
					}

					// DAGModeReflect: reflection is already injected structurally by injectReflectionNodes
				}
			}

			// A tool's calls are read together once they have all landed, and
			// only when one of them failed. This is the one moment the set can
			// be compared — after the last sibling and before launchReady lets
			// anything downstream read them — and it is why the check sits
			// here rather than in the tool, which sees one reply at a time and
			// cannot tell a refusal from an answer without knowing the service.
			//
			// Cheap by construction: one call for the whole group, never one
			// per reply, and none at all for a group where everything worked.
			if group, ready := completedGroup(graph, node); ready && budget.TryObserverCall() {
				inflight++
				go a.fireGroupReview(ctx, group, graph, budget, completionCh, trigger)
			}

			// Check for human interjection before launching new nodes.
			// If injected, pending nodes are gated — they'll launch after
			// the interjection reflection completes.
			injectInterjection()
			launchReady()
		}
	}

	log.Printf("[dag] all nodes complete (total=%d)", graph.NodeCount())

	return &scheduleOutcome{
		Intent:              intent,
		ReflectionOutcome:   reflectionOutcome,
		ReflectionAggregate: reflectionAggregate,
	}, nil
}

/*
 * SyncResult contains the full output from a synchronous DAG investigation.
 * desc: Wraps the synthesized outcome, recommended follow-up actions, and
 *       any capability gaps declared by the planner.
 */
type SyncResult struct {
	Outcome string           // synthesized response text
	Actions []ActuatorAction // recommended follow-up actions (caller decides whether to execute)

	// Data is the structured result from an application-supplied Answer, and
	// nil for a run the built-in aggregator answered. Opaque: the engine carries
	// it from AnswerResult.Data to here and never reads it. The caller casts it
	// back to its own type.
	Data any

	Nodes    int // total DAG nodes executed
	LLMCalls int // total LLM round-trips

	// RunID is this run, not the caller's reference: one reference can produce
	// several runs. Here because a caller that records something after a run —
	// a record, a note, a row of its own — has no other way to say which run
	// produced it. The caller's own context does not carry it; the run stamps
	// it on its own.
	RunID string
	// Trace is the final DAG node snapshot (the same JSON shape the browser
	// streams and renders), serialized so the caller can persist it against the
	// assistant message. Server-authoritative: the trace is saved by the process
	// that produced it, not round-tripped back from the browser's live buffer
	// (which a replan's start-event clear or an SSE/return race can empty).
	Trace json.RawMessage

	// NotAdmitted is true when the application's admission check refused this
	// run. Outcome then carries its reason and no work was done. A separate
	// field rather than a sentinel in Outcome, so a caller can tell a refusal
	// from an answer without reading the text.
	NotAdmitted bool
}

/*
 * RunDAGSync runs the full DAG pipeline synchronously and returns the result.
 * desc: Used by the API to route queries through the parallel investigation
 *       engine. Actions are returned to the caller — not auto-executed.
 *       Handles ExecutiveConversationalError by falling back to direct LLM chat
 *       or returning the planner's conversational text.
 * param: ctx - context for the investigation.
 * param: trigger - the investigation trigger.
 * return: SyncResult pointer with outcome, actions, and gaps, or error.
 */
func (a *Agent) RunDAGSync(ctx context.Context, trigger Trigger) (*SyncResult, error) {
	// Attribute every LLM call in this run to a token-usage category. Principal
	// (if any) is already on ctx from the API boundary and is preserved.
	ctx = a.tagTokens(ctx, trigger)
	// Carry the per-request model selection so every heavy/light lane call in
	// this run routes to the host-chosen provider (see model_route.go).
	ctx = withLaneSelection(ctx, laneSelectionFromTrigger(trigger))
	// The run's identity, before admission and before the model calls that
	// route and classify — all of which write traces and had nothing to name
	// them after. setupDAGPipeline takes this same id rather than making its
	// own, so the graph and the traces agree.
	runID := newRunID(trigger.ID)
	ctx = withRunID(ctx, runID)
	startTime := time.Now()

	// Run admission before any work or model call, and before the branch below,
	// so it covers every mode. It used to sit under the branch, which meant a
	// run asking for the ReAct loop was never put to the application at all —
	// the mode a request names decided whether the application's rule applied.
	//
	// A refusal comes back as a result carrying the application's own wording
	// rather than an error: the caller asked for work the application had
	// already decided not to do, which is not a failure. It is also recorded,
	// because "we chose not to" is the likeliest answer to the question the run
	// record exists for — why nothing happened — and it was the one exit that
	// left nothing behind.
	if ok, reason := a.admit(trigger); !ok {
		log.Printf("[dag] run not admitted (type=%s id=%s): %s", trigger.Type, trigger.ID, reason)
		a.recordRun(trigger, startTime, nil, nil, trigger.Intent(),
			Conclusion{Outcome: reason, Status: "not_admitted"})
		return &SyncResult{Outcome: reason, NotAdmitted: true}, nil
	}

	// Route to ReAct loop if mode=react
	if trigger.DAGMode == "react" {
		return a.RunReActSync(ctx, trigger)
	}

	log.Printf("[dag] sync run: type=%s id=%s source=%s",
		trigger.Type, trigger.ID, trigger.Source)

	// Mark the start of a new run in the worklog. History is preserved so
	// Holmes can see prior failures, but the separator lets the reflector
	// distinguish current vs stale evidence.
	markRunStart(a.cfg.MetadataDir, trigger.SessionID)
	rotateServiceLogs(a.cfg.Workspace)

	graph, budget, cleanup := a.setupDAGPipeline(trigger, runID)
	defer cleanup()

	// Two contexts, because the time limit is on the WORK and never on the
	// answer. execCtx carries the wall clock and bounds planning and node
	// execution; answerCtx is the caller's own, so a run whose clock ran out
	// still writes what it found instead of returning an error to somebody who
	// asked a question. A caller that has gone away cancels both.
	//
	// WallClock was previously stored on the Budget and read by nobody, so the
	// setting had no effect and the only thing that ever stopped a run was the
	// caller hanging up. Zero still means no limit, which is what a host that
	// leaves it unset gets.
	answerCtx, cancelAnswer := context.WithCancel(ctx)
	defer cancelAnswer()
	execCtx := answerCtx
	if budget.WallClock > 0 {
		var cancelExec context.CancelFunc
		execCtx, cancelExec = context.WithTimeout(answerCtx, budget.WallClock)
		defer cancelExec()
	}

	pr, err := a.runPlanAndSchedule(execCtx, trigger, graph, budget)
	if err != nil {
		// If planner returned conversational text, surface it as the outcome
		// instead of failing the whole pipeline. This handles vague queries.
		var convErr *ExecutiveConversationalError
		if errors.As(err, &convErr) {
			if convErr.Text != "" {
				// notrecorded: answered without planning — see TestEveryRunExitRecords
				return &SyncResult{Outcome: convErr.Text}, nil
			}
			// Empty plan with no text — make a direct LLM call for conversational response.
			// Inject any skill guidance the preflight identified so the chat response
			// has domain context (e.g. webdeveloper skill for web questions).
			log.Printf("[dag] empty plan, falling back to direct response")
			query := ""
			if trigger.Data != nil {
				var d map[string]string
				if json.Unmarshal(trigger.Data, &d) == nil {
					query = d["query"]
				}
			}
			query = chatQuery(trigger)
			if query != "" {
				chatPrompt := a.soulPrompt
				if graph != nil && graph.Context != nil {
					gateResp, gerr := graph.Context.Get(answerCtx, ContextRequest{
						ReturnSources: Sources(SkillGuidance(nil)),
						MaxBudget:     4000,
					})
					if gerr == nil {
						if sg := gateResp.Sources[SourceSkillGuidance]; sg != "" {
							chatPrompt += "\n\n## Active Skill Guidance\n\n" + sg
						}
					} else {
						log.Printf("[dag] chat-mode skill guidance gate fetch failed: %v", gerr)
					}
				}
				chatPrompt += "\n\nThis turn is a quick conversational reply — no tools were invoked because the request was classified as chat. If the user is actually asking for an action (run X, fetch Y, build Z, fix this, find that, compute, search, edit a file, restart a service, anything imperative), do NOT refuse on the basis that you can't execute tools. The full toolchain (compute, file_*, bash, web_*, service, edit_file, etc.) IS available — the next turn will route through it. Acknowledge what they're asking for, restate it as an actionable request, and tell them to confirm so the next turn can run it. Never say 'I cannot execute code' or 'I have no tools' — that is false in this system."
				chatPrompt += "\n\n## Output format\n" + a.FormatRule()

				// The answer, as a node. Same call as before; it now has a place
				// on the graph, which is what gives an interjection somewhere to
				// land and a trace something to show.
				answer, chatID, llmErr := a.runChatNode(answerCtx, trigger, graph, query, chatPrompt)
				if llmErr == nil {
					// A steer typed while that answer was being written. Recorded
					// beside the chat node and handed to the aggregator, which
					// writes the reply the user actually reads — the reflector's
					// outcome is a capped summary, right for a status line and
					// wrong for an answer (see chat.go, which forces agg_mode 2
					// on escalation for the same reason).
					//
					// No interjection is the common case and costs nothing extra:
					// the chat node's own answer is returned as it always was.
					if msg, ok := pendingInterjection(answerCtx); ok {
						addInterjectionNode(graph, msg)
						if coordinated, aggErr := a.coordinateChatAnswer(answerCtx, trigger, graph, resolvedChatIntent(trigger), query, answer, msg); aggErr == nil && coordinated != "" {
							answer = coordinated
						} else if aggErr != nil {
							log.Printf("[dag] chat lane: coordinating the interjection failed, answering without it: %v", aggErr)
						}
					}
					_ = chatID
					// notrecorded: answered without planning — see TestEveryRunExitRecords
					return &SyncResult{Outcome: answer}, nil
				}
			}
			// notrecorded: answered without planning — see TestEveryRunExitRecords
			return &SyncResult{Outcome: "I'm not sure how to help with that. Could you be more specific?"}, nil
		}
		a.recordRun(trigger, startTime, graph, budget, trigger.Intent(), Conclusion{Outcome: "plan_or_schedule_failed", Status: "failed"})
		return nil, err
	}
	resolvedIntent := pr.Intent

	// Aggregator: agg_mode -1=auto (reflector decides), 0=skip, 1=executor model, 2=reasoning model
	aggMode := trigger.AggMode
	var outcome string
	var actions []ActuatorAction
	var labels map[string]string
	// recordLine is what the run record keeps, which may be shorter than the
	// answer itself. Empty until an answer says otherwise; the outcome is used.
	var recordLine string
	// data is the application's structured result, carried back untouched.
	var data any

	// Auto mode: decide who writes the final answer. A sizable/complex query
	// (preflight said so, or the run structurally fanned out) MUST end with the
	// aggregator — a full synthesis with the honesty framing and 2x token budget —
	// rather than a short reflector summary. The reflector only wins here when it
	// couldn't gather usable evidence: then its honest "couldn't get the data"
	// outcome is the right answer and an aggregator pass over emptiness is skipped.
	hasCompute := graph.HasNodeOfType(NodeCompute)
	needsSynthesis := graph.Preflight != nil && graph.Preflight.NeedsSynthesis
	fanout := a.runFanout(graph)
	complex := needsSynthesis || fanout >= complexFanoutFloor // a real gather, not a lookup
	if aggMode == -1 && pr.ReflectionOutcome != "" {
		var reason string
		aggMode, reason = decideAutoAggMode(hasCompute, complex, a.hasUsableEvidence(graph),
			triggerIsAwaited(trigger), pr.ReflectionAggregate)
		log.Printf("[dag] auto agg: %s (needs_synthesis=%v, fanout=%d)", reason, needsSynthesis, fanout)
	}

	// If reflection concluded AND aggregator is disabled, use reflection outcome directly
	if pr.ReflectionOutcome != "" && aggMode == 0 {
		log.Printf("[dag] skipping aggregator (agg_mode=0, reflection concluded)")
		outcome = pr.ReflectionOutcome
		a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: "aggregator", Node: &NodeInfo{ID: "aggregator", Type: "aggregator", State: "resolved", Tag: "synthesize (skipped)"}})
		a.broadcastDAGEvent(graph, DAGEvent{Type: "outcome", Text: outcome})
	} else {
		// Only the caller going away stops the answer being written. An expired
		// wall clock does not: it ended the gathering, and the evidence already
		// on the graph is what the answer is made from. This used to test the
		// execution context, so a run that ran out of time returned an error and
		// the person waiting on it got nothing at all — the failure this split
		// exists to prevent.
		if answerCtx.Err() != nil {
			a.recordRun(trigger, startTime, graph, budget, resolvedIntent, Conclusion{Outcome: "caller_gone", Status: "abandoned"})
			return nil, fmt.Errorf("the caller went away before the answer was written: %w", answerCtx.Err())
		}
		if execCtx.Err() != nil {
			log.Printf("[dag] wall clock expired after %s — answering from the evidence gathered so far", budget.WallClock)
		}
		// Aggregator is exempt from budget — it must always run to give the user a response
		budget.TrySpawnNode("", true) // charge if possible, but don't block

		// Select LLM lane based on agg_mode, then route to the host-selected
		// provider within that lane (model_route.go). aggModel is the routed
		// model id ("" ⇒ the lane client's own default).
		//
		// The answer lane, not the heavy lane: this call writes the final answer,
		// which is what a pinned answer model is for. answerLane falls back to
		// the heavy lane when none is pinned, so an unset answer model behaves
		// exactly as before.
		aggLane := Answer
		if aggMode == 1 {
			aggLane = Light // the executor model, only when explicitly requested
		}
		log.Printf("[dag] aggregator using %s model (agg_mode=%d)", aggLane, aggMode)

		a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: "aggregator", Node: &NodeInfo{ID: "aggregator", Type: "aggregator", State: "running", Tag: "synthesize"}})

		var aggErr error
		aggCtxResp2, actxErr2 := graph.Context.Get(answerCtx, ContextRequest{
			ReturnSources: Sources(
				NodeReturns("all"),
				Worklog(30, "all"),
			),
			MaxBudget: 12000,
		})
		if actxErr2 != nil {
			log.Printf("[dag] aggregator2 context build failed: %v", actxErr2)
			aggCtxResp2 = &ContextResponse{Sources: map[string]string{}}
		}
		// The application's own answer, when it supplies one — see answer.go.
		// The lane chosen above applies to the built-in aggregator; an
		// application that writes its own answer chooses its own model.
		answered, ok, ansErr := a.writeAnswer(answerCtx, AnswerRequest{
			Trigger: trigger, Graph: graph, Evidence: aggCtxResp2,
			Intent: resolvedIntent, History: trigger.History,
		})
		if ansErr != nil {
			a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: "aggregator", Node: &NodeInfo{ID: "aggregator", Type: "aggregator", State: "failed", Tag: "synthesize", Error: ansErr.Error()}})
			a.recordRun(trigger, startTime, graph, budget, resolvedIntent, Conclusion{Outcome: "aggregator_failed", Status: "failed"})
			return nil, fmt.Errorf("supplied answer failed: %w", ansErr)
		}
		if ok {
			outcome, actions = answered.Text, answered.Actions
			labels, data = answered.Labels, answered.Data
			if recordLine = answered.Summary; recordLine == "" {
				recordLine = answered.Text
			}
		} else {
			outcome, actions, aggErr = a.runAggregator(answerCtx, trigger, graph, resolvedIntent, trigger.History, aggLane, aggCtxResp2)
			if aggErr != nil {
				a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: "aggregator", Node: &NodeInfo{ID: "aggregator", Type: "aggregator", State: "failed", Tag: "synthesize", Error: aggErr.Error()}})
				a.recordRun(trigger, startTime, graph, budget, resolvedIntent, Conclusion{Outcome: "aggregator_failed", Status: "failed"})
				return nil, fmt.Errorf("aggregator failed: %w", aggErr)
			}
		}
		a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: "aggregator", Node: &NodeInfo{ID: "aggregator", Type: "aggregator", State: "resolved", Tag: "synthesize"}})
	}

	elapsed := time.Since(startTime)
	// What the caps cut, when any did. Silence means the run was not starved,
	// which is the answer worth having — a cap that never fires costs nothing
	// however small it looks.
	if report := graph.CapReport(); report != "" {
		log.Printf("[dag] caps: %s", report)
	}
	log.Printf("[dag] sync run complete in %s (id=%s, nodes=%d, llm=%d)",
		elapsed.Round(time.Millisecond), trigger.ID, graph.NodeCount(), budget.LLMCount())

	// Record investigation in event store
	if recordLine == "" {
		recordLine = outcome
	}
	a.recordRun(trigger, startTime, graph, budget, resolvedIntent, Conclusion{Outcome: recordLine, Labels: labels, Status: "completed"})

	// Snapshot the final graph so the caller can persist the trace server-side.
	// This is the same node list the "done" event broadcasts to the browser.
	traceJSON, _ := json.Marshal(graph.Snapshot())

	return &SyncResult{
		Outcome:  outcome,
		Actions:  actions,
		Data:     data,
		Nodes:    graph.NodeCount(),
		LLMCalls: int(budget.LLMCount()),
		Trace:    traceJSON,
		RunID:    runID,
	}, nil
}

// toolReportedFailure reports whether a node completed with a failure, read from
// its envelope. Drives the self-repair loop: a failure here marks the node
// errored, which reaches the reflector, which can send the run to the debugger.
//
// It applies to every tool, not only the shell. A tool has two ways to say it
// failed — return a Go error, or return an envelope whose status is error — and
// only the first used to reach the node. So a step that fetched a page and got
// a 400 back resolved like a success: its error message was filed as evidence,
// the run's failure list stayed empty, and no repair could be asked for because
// nothing had failed. Narrowing this to one tool was never the intent; it was
// where it was first needed.
//
// It used to fall back to searching the result text for "bash_error":true when
// a node had no typed body. Nothing has written that string since the tool
// returned envelopes, and the one path that arrived without a body — a step
// dispatched to another machine — now parses one, so the search could only ever
// have matched a tool whose output happened to contain the words.
//
// KNOWN WART: a tool that refuses because it is not available here — no store
// configured, a plugin not compiled in — reports the same status as one that
// tried and could not. Both mark the node failed, and only the second is worth
// a repair. Telling them apart needs a status of its own, which is a change
// across every tool in this engine and in whatever embeds it.
/*
 * retryGaveUp is the completion a retry sends when it will not, or could not,
 * repair the step.
 * desc: Two jobs. It never reports success — a retry that declines has not made
 *       the step work — and it carries back what the tool DID produce, which the
 *       old bail-out dropped by sending the node id and an error alone.
 *
 *       It used to also fold in a failure reported in the tool's result, because
 *       comp.Err was nil on exactly the path that reached here. completionOf now
 *       rolls that up where the tool's outcome is packed, so by the time a
 *       completion exists its failure is in one place. The guard below stays as
 *       the floor: reporting nil here is what turned a failed step into a
 *       successful one, and it is worth making impossible rather than unlikely.
 * param: comp - the completion that arrived at the retry.
 * return: the same completion, with a failure that is never nil.
 */
func retryGaveUp(comp nodeCompletion) nodeCompletion {
	err := comp.Err
	if err == nil {
		err = errors.New("the step did not succeed and no repair was possible")
	}
	return nodeCompletion{NodeID: comp.NodeID, Err: err, Body: comp.Body, Result: comp.Result}
}

/*
 * toolReportedFailure reports whether a tool said it failed in its result.
 * desc: Reads the status a typed tool sets, which is how a tool reports a
 *       failure it recognised rather than one that stopped it running. Called
 *       once, by completionOf, so the answer is rolled into the completion and
 *       no later stage has to know there were ever two places to look.
 * param: comp - the completion to inspect.
 * return: the failure and true, or nil and false.
 */
// nodeEvidence is what a step produced as text, for a human-facing line.
// Falls back to the raw result for a body that has none.
func nodeEvidence(n *Node, comp nodeCompletion) string {
	if n != nil && n.Body != nil {
		if e := n.Body.Evidence(); e != "" {
			return e
		}
	}
	if comp.Body != nil {
		if e := comp.Body.Evidence(); e != "" {
			return e
		}
	}
	return comp.Result
}

func toolReportedFailure(comp nodeCompletion) (error, bool) {
	tb, ok := comp.Body.(toolMessageBody)
	if !ok {
		return nil, false
	}
	env := tb.Envelope()
	if env.Status != toolapi.StatusError {
		return nil, false
	}
	detail := env.Detail
	if detail == "" {
		detail = "the tool reported a failure and gave no reason"
	}
	return fmt.Errorf("%s failed: %s", env.Type, Text.TruncateLog(detail, 300)), true
}

// debugProblem reports whether a node completed as a debug envelope and returns
// its problem brief — from the typed body (Kind=="debug") when the tool used
// ExecuteTyped, else the legacy {type:"debug"} string. Triggers the Holmes graft.
func debugProblem(comp nodeCompletion) (string, bool) {
	if tb, ok := comp.Body.(toolMessageBody); ok {
		env := tb.Envelope()
		if env.Type != "debug" {
			return "", false
		}
		var d struct {
			Problem string `json:"problem"`
		}
		_ = json.Unmarshal(env.Data, &d)
		return d.Problem, true
	}
	var dbg struct {
		Type    string `json:"type"`
		Problem string `json:"problem"`
	}
	if json.Unmarshal([]byte(comp.Result), &dbg) == nil && dbg.Type == "debug" {
		return dbg.Problem, true
	}
	return "", false
}

/*
 * extractBashStdout pulls the captured stdout out of a bash node's Result.
 * desc: Bash nodes from non-erroring runs return their combined output as a
 *       plain string (not JSON). Error runs return the JSON blob with
 *       stdout/stderr fields. This helper normalises both.
 */
func extractBashStdout(result string) string {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return ""
	}
	// Typed bash: envelope carrying stdout/stderr in data.
	if msg, ok := toolapi.ParseToolMessage(trimmed); ok {
		var d struct {
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
		}
		if json.Unmarshal(msg.Data, &d) == nil {
			if d.Stdout != "" {
				return d.Stdout
			}
			if d.Stderr != "" {
				return d.Stderr
			}
		}
		return msg.Content
	}
	if strings.HasPrefix(trimmed, "{") && strings.Contains(trimmed, `"bash_error"`) {
		var bashErr struct {
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
		}
		if err := json.Unmarshal([]byte(trimmed), &bashErr); err == nil {
			if bashErr.Stdout != "" {
				return bashErr.Stdout
			}
			return bashErr.Stderr
		}
	}
	return trimmed
}

/*
 * mergeJSONField parses a JSON object string, sets key=value, returns the
 * reserialised string. Used to graft a field onto an already-stored node
 * Result after its creation (e.g. folding exec stdout onto a compute's
 * result so downstream ${step.N.field} substitution can reach it).
 */
func mergeJSONField(raw, key, value string) (string, error) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return "", err
	}
	if obj == nil {
		obj = map[string]any{}
	}
	obj[key] = value
	b, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// retryDetail is what a failed step reported, for the code deciding whether to
// try it again and for the fixer that rewrites the command.
//
// The engine's own sentence for a failed command is "command failed: exit 1:
// exit status 1", which names no cause and gives a fixer nothing to change. The
// step had already said why: beside that exit code sat "the input device is not
// a TTY", naming the one flag that had to go. A run spent three replans and
// concluded the machine was locked down, on a command that needed a flag
// removed.
//
// The node's own account is preferred where it has one, and the engine's is the
// fallback for a failure that carries nothing else.
func retryDetail(n *Node, fallback string) string {
	if d := strings.TrimSpace(extractFailureDetail(n)); d != "" {
		return d
	}
	return fallback
}

// classifyRetryTier determines what kind of retry (if any) is appropriate.
// Returns "skip" (no retry), "blind" (rerun same command), or "twotime" (LLM fix,
// up to two attempts — the second informed by how the first one failed).
func classifyRetryTier(errMsg string) string {
	lower := strings.ToLower(errMsg)

	// Tier 1: skip — structural errors, no retry will help
	skipPatterns := []string{
		"ejsonparse", "enoent", "no such file or directory", "syntaxerror", "syntax error",
		"gate:", "permission denied", "eacces",
		"module not found", "cannot find module",
		"command not found",
		"splice", "edit out of bounds",
		"timed out", "command timed out", // a 60s timeout won't succeed on retry
		// A broken ${node.<id>.field} wiring is structural — an LLM shell fix
		// can't resolve a dependency; it only mangles the placeholder into a
		// real (invalid) shell expansion and loops. Fail it cleanly instead.
		"dependency injection failed",
	}
	for _, p := range skipPatterns {
		if strings.Contains(lower, p) {
			return "skip"
		}
	}

	// Tier 2: blind — transient errors, just rerun
	blindPatterns := []string{
		"connection refused", "econnrefused", "econnreset",
		"etimedout",     // network timeout (not command timeout)
		"exit status 7", // curl: couldn't connect
		"npm err! network", "fetch failed",
		"rate limit", "http 429", "http 503",
	}
	for _, p := range blindPatterns {
		if strings.Contains(lower, p) {
			return "blind"
		}
	}

	// Tier 3: twotime — everything else gets up to two cheap LLM fix attempts
	return "twotime"
}

/*
 * retryBackoff is how long a blind retry should wait before rerunning.
 * desc: Most of the blind tier is a condition that may already be gone — a
 *       refused connection, a reset, a network blip — and rerunning at once is
 *       right. Two of them are not: 429 and 503 are the other end saying it is
 *       being asked for too much. Rerunning those immediately is the one thing
 *       certain to fail, and it spends the node's single retry proving it.
 *
 *       Measured: a 429 from explorer.solana.com was reran 51ms later and
 *       returned 429 again.
 *
 *       A fixed pause, not a growing one — there is only ever one retry per
 *       node, so there is no sequence to back off along. Long enough to outlast
 *       a per-second limit, short enough that a run waiting on it is still
 *       answering.
 * param: errMsg - the failure that classified as blind.
 * return: how long to wait, or zero to rerun at once.
 */
func retryBackoff(errMsg string) time.Duration {
	lower := strings.ToLower(errMsg)
	for _, p := range []string{"rate limit", "http 429", "http 503", "too many requests"} {
		if strings.Contains(lower, p) {
			return 5 * time.Second
		}
	}
	return 0
}

// twotimeRetry fixes a failed command with up to two executor LLM calls: the
// first from the original error, the second from however that fix failed.
// Context: just the command + error. No worklog, no blueprint, no evidence.
// The LLM returns a fixed command which is gate-checked and re-executed.
func (a *Agent) twotimeRetry(ctx context.Context, node *Node, comp nodeCompletion,
	graph *Graph, budget *Budget, ch chan nodeCompletion, errMsg string,
	intent gates.Intent, scope *ResolvedScope) {

	defer a.guardNodeCompletion("twotimeRetry", comp.NodeID, ch)

	// Through the graph, not the map: the retry below rewrites this parameter
	// from its own goroutine while the trace reads it. See Graph.SetParam.
	rawCommand, _ := graph.Param(node.ID, "command")
	command, _ := rawCommand.(string)
	if command == "" {
		ch <- retryGaveUp(comp)
		return
	}

	// A failed command that STILL carries kaiju ${node.<id>.field} placeholders
	// never had its dependency outputs injected — that's a DAG wiring failure,
	// not a shell bug. The dispatcher resolves these via substituteTemplates
	// before executing; this retry path historically did not, so the raw
	// placeholder reached the shell as "sh: Bad substitution", and the
	// shell-fixer LLM below (which can't tell a kaiju placeholder from a shell
	// variable) would just fiddle with quoting forever. Resolve the templates
	// here instead — the dep may have completed since the first attempt — and
	// fail cleanly with the injection error if it genuinely can't resolve. No LLM.
	if nodeTemplateRe.MatchString(command) {
		if err := substituteTemplates(node, graph, a.registry); err != nil {
			log.Printf("[dag] twotime: %s has unresolved templates (dependency injection, not a shell fix): %v", node.ID, err)
			ch <- nodeCompletion{NodeID: comp.NodeID, Err: fmt.Errorf("dependency injection failed: %w", err)}
			return
		}
		appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "TEMPLATE_REINJECT", "resolved ${node...} placeholders on retry")
		a.execBashParams(ctx, node, comp, ch)
		return
	}

	// Two attempts, not one. The first fix is a guess from the error alone; if
	// it runs and fails DIFFERENTLY, that new error is information the first
	// attempt did not have, and a second fix built on it often lands. Asking the
	// same question twice would not — temperature is 0, so an identical request
	// returns an identical answer. It is the new error that makes the second
	// attempt worth its call.
	//
	// Bounded at two: a third attempt has diminishing odds and unbounded retry
	// on a shell command is how a run burns its budget on one broken step.
	lastCmd, lastErr := command, errMsg
	for attempt := 1; attempt <= shellFixAttempts; attempt++ {
		fixed, ok := a.askShellFix(ctx, node.ID, node.Tag, lastCmd, lastErr)
		if !ok {
			break // no usable fix; fall through to the original error
		}

		// Gate-check the fixed command through the normal IGX path. A fix may
		// be a bigger action than the command it replaces, and the tier that
		// admitted the first one does not admit the second by inheritance.
		graph.SetParam(node.ID, "command", fixed)
		// The fixed command alone, rather than the node's live parameter map.
		// It is what the gate is being asked about, and reading the map here
		// would read a value another goroutine may be rendering.
		fixedImpact := a.intentRegistry.ResolveToolIntent("bash", nil,
			map[string]any{"command": fixed})
		scopeCap := -1
		if scope != nil {
			if cap, ok := scope.MaxImpact["bash"]; ok {
				scopeCap = cap
			}
		}
		if gateErr := a.gate.CheckTriadWithScope(intent, "bash", fixedImpact, scopeCap); gateErr != nil {
			log.Printf("[dag] shell fix blocked by gate: %v", gateErr)
			graph.SetParam(node.ID, "command", command) // restore original
			break
		}

		log.Printf("[dag] shell fix (attempt %d) %q → %q", attempt,
			Text.TruncateLog(lastCmd, 80), Text.TruncateLog(fixed, 80))
		appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "SHELL_FIX",
			fmt.Sprintf("attempt %d: %s → %s", attempt,
				Text.TruncateLog(lastCmd, 60), Text.TruncateLog(fixed, 60)))

		// Resolve any ${node.<id>.field} templates before executing — exactly
		// like the dispatcher — so a placeholder can never reach the shell.
		if err := substituteTemplates(node, graph, a.registry); err != nil {
			ch <- nodeCompletion{NodeID: comp.NodeID, Err: fmt.Errorf("dependency injection failed: %w", err)}
			return
		}

		result, execErr := a.runBashParams(ctx, node)
		if execErr == nil {
			ch <- nodeCompletion{NodeID: comp.NodeID, Result: result}
			return
		}
		// Failed again. Feed THIS error to the next attempt — the whole reason a
		// second attempt is worth making.
		lastCmd, lastErr = fixed, execErr.Error()
		log.Printf("[dag] shell fix attempt %d still failed: %s", attempt,
			Text.TruncateLog(lastErr, 120))
	}

	// Nothing landed. Report the ORIGINAL error: it describes the command the
	// planner actually wrote, which is what a reader is looking for.
	graph.SetParam(node.ID, "command", command)
	ch <- retryGaveUp(comp)
}

// shellFixAttempts is how many times a failing shell command is rewritten and
// re-run. Two: the first fix guesses from the original error, the second is
// informed by how the first one failed.
const shellFixAttempts = 2

/*
 * askShellFix asks the model for a corrected command.
 * desc: One light-lane call. Returns the command and true, or false when
 *       nothing usable came back — an empty reply, or one that reproduces the
 *       command it was asked to fix.
 * param: ctx - the run context.
 * param: objective - what the step is for; the node's tag.
 * param: command - the command that failed.
 * param: errMsg - what it returned.
 * return: the corrected command, and whether it is usable.
 */
func (a *Agent) askShellFix(ctx context.Context, nodeID, objective, command, errMsg string) (string, bool) {
	// Traced like every other stage that reasons. Without this the retry was the
	// one LLM call in a run that left no record: the prompt trace showed the
	// command failing and then a different command succeeding, with nothing in
	// between to say what was asked or why the answer looked like that.
	ctx = withTrace(ctx, TraceID{
		NodeID:   nodeID,
		NodeType: "twotime",
		Tag:      "shell fix",
		Input: map[string]string{
			"objective": objective,
			"command":   command,
			"error":     Text.TruncateLog(errMsg, 300),
		},
	})
	resp, err := a.completeLight(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: shellFixSystemPrompt},
			{Role: "user", Content: shellFixPrompt(objective, command, errMsg)},
		},
		Temperature: 0.0,
		MaxTokens:   256,
	})
	if err != nil || len(resp.Choices) == 0 {
		return "", false
	}
	fixed := cleanShellFix(resp.Choices[0].Message.Content)
	if fixed == "" || fixed == command {
		return "", false
	}
	return fixed, true
}

// execBashParams runs the bash tool with the node's current params (same path as
// normal dispatch) and reports the outcome. Shared by the retry paths.
func (a *Agent) execBashParams(ctx context.Context, node *Node, comp nodeCompletion, ch chan nodeCompletion) {
	result, execErr := a.runBashParams(ctx, node)
	if execErr != nil {
		ch <- nodeCompletion{NodeID: comp.NodeID, Err: execErr}
	} else {
		ch <- nodeCompletion{NodeID: comp.NodeID, Result: result}
	}
}

// runBashParams runs the bash tool and RETURNS the outcome instead of reporting
// it. Separated from execBashParams so a caller that wants to act on a failure —
// the second fix attempt — can see it without racing the completion channel.
func (a *Agent) runBashParams(ctx context.Context, node *Node) (string, error) {
	sk, ok := a.registry.Get("bash")
	if !ok {
		return "", fmt.Errorf("bash tool not found")
	}
	return sk.(interface {
		Execute(context.Context, map[string]any) (string, error)
	}).Execute(ctx, node.Params)
}

// graftComputeExecution wires an execute bash node — the one that runs the
// code compute just generated — downstream of a shallow-mode top-level compute
// node. Returns the grafted nodes so the caller can rewire downstream deps.
//
// The execute node tees its combined output to /tmp/kaiju_<node>.out.
func (a *Agent) graftComputeExecution(graph *Graph, comp *Node, compID, execCmd string, budget *Budget) []*Node {
	var grafted []*Node
	if execCmd == "" {
		return grafted
	}
	if !budget.TrySpawnNode("bash", false) {
		log.Printf("[dag] shallow compute → budget exhausted, skipping exec graft for %s", comp.ID)
		return grafted
	}

	outFile := fmt.Sprintf("/tmp/kaiju_%s.out", comp.ID)
	wrapped := fmt.Sprintf("( %s ) >%s 2>&1; rc=$?; cat %s; exit $rc", execCmd, outFile, outFile)
	execTag := "exec_" + sanitizeTag(comp.Tag)
	if execTag == "exec_" {
		execTag = "exec_" + comp.ID
	}
	execNode := &Node{
		Type:      NodeTool,
		ToolName:  "bash",
		Params:    map[string]any{"command": wrapped, "timeout_sec": 120},
		DependsOn: []string{compID},
		SpawnedBy: compID,
		Tag:       execTag,
		Source:    "builtin",
	}
	execID := graph.AddNode(execNode)
	graph.AddChild(compID, execID)
	// The script this child runs is what produces the parent's `output`, and a
	// step wired to that field depends on the parent — so it would otherwise be
	// ready at the same moment as this child and could run first, reading a
	// field that has not been written yet.
	if waited := graph.WaitAlsoOn(compID, execID, "output"); len(waited) > 0 {
		log.Printf("[dag] %s now also waits for %s, which produces %s.output",
			strings.Join(waited, ", "), execID, compID)
	}
	grafted = append(grafted, execNode)
	a.broadcastDAGEvent(graph, DAGEvent{Type: "node", NodeID: execID, Node: graph.SnapshotNode(execID)})

	// Note: we used to graft a `verify_<tag>` bash node here that ran a
	// coder-supplied bash check against the captured output. That validator
	// was authored *inside the coder LLM call*, before the script had ever
	// run — i.e. the LLM was predicting what its own code would print, then
	// asserting against that prediction. The prediction was wrong constantly
	// (off-by-N length cutoffs, exact-key greps, jq paths that never matched
	// the actual output shape), each failure spawned a full Holmes+debugger
	// cycle, and the cycle's path-hallucinations cascaded further. Removed.
	//
	// Failure detection now relies on:
	//   1. bash exit code on the exec node — non-zero already routes through
	//      toolReportedFailure in the scheduler and is treated as failure.
	//   2. the reflector — it reads the exec node's captured stdout from
	//      comp.Result with full context (goal, code, output) and decides
	//      continue / investigate / conclude. The reflector is strictly
	//      better positioned than a one-shot bash grep for this judgment.
	return grafted
}

// Dispatcher functions (fireNode, resolveInjections, extractJSONField,
// executeToolNode, toolThrottle) are in dispatcher.go.

// shellFixSystemPrompt asks for a corrected command and nothing else.
//
// The objective is included because a command that no longer serves the goal is
// not a fix: `find / -name x` failing on permission errors can be "fixed" into
// `find /tmp -name x`, which succeeds and answers a different question.
const shellFixSystemPrompt = `Fix the shell command based on the error.

You are given a JSON object with three fields: the objective the command
serves, the command that ran, and the result it produced.
Return ONLY the corrected command — no JSON, no explanation, no code fence.

The fix must still serve the objective. A command that succeeds by doing
something narrower or different is not a fix. If the error cannot be fixed by
changing the command — a permission, a missing host, a network failure — return
the command unchanged.`

/*
 * shellFixPrompt renders what the model is shown, as objective/command/result.
 * desc: The same three things every time, in the same order, so the reply's
 *       shape is predictable. Extracted from the retry path so it can be tested
 *       without a model.
 * param: objective - what the step is for; the node's tag. May be empty.
 * param: command - the command that failed.
 * param: errMsg - what it returned; truncated, as the caller's budget requires.
 * return: the user message.
 */
func shellFixPrompt(objective, command, errMsg string) string {
	obj := strings.TrimSpace(objective)
	if obj == "" {
		obj = "(not stated)"
	}
	b, err := json.Marshal(shellFixRequest{
		Objective: obj,
		Command:   command,
		Result:    Text.TruncateLog(errMsg, 300),
	})
	if err != nil {
		// Marshalling three strings cannot realistically fail, but a prompt is
		// worth more than an error here: fall back to the labelled form rather
		// than send nothing.
		return fmt.Sprintf("Objective:\n%s\n\nCommand:\n%s\n\nResult:\n%s",
			obj, command, Text.TruncateLog(errMsg, 300))
	}
	return string(b)
}

// shellFixRequest is what the fixer is shown: what the step is FOR, what was
// RUN, and what came BACK. Named fields in a fixed order so the reply's shape
// is predictable, and the same three the tool-side repair would use.
type shellFixRequest struct {
	Objective string `json:"objective"`
	Command   string `json:"command"`
	Result    string `json:"result"`
}

/*
 * cleanShellFix reduces a model reply to a bare command.
 * desc: Strips a fence or stray backticks the instruction asked it not to send,
 *       and takes the first line — a reply that explains itself puts the command
 *       first. Extracted so the shapes a model actually returns can be tested.
 * param: raw - the reply.
 * return: the command, or empty when nothing usable came back.
 */
func cleanShellFix(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		if _, rest, ok := strings.Cut(s, "\n"); ok {
			s = rest
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
	}
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "`"))
}
