package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/gates"
	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/agent/tools"
	"github.com/Compdeep/kaiju/internal/compat/store"
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
 * param: trigger - the investigation trigger (used for alert ID).
 * return: graph, budget, and cleanup function.
 */
func (a *Agent) setupDAGPipeline(trigger Trigger) (*Graph, *Budget, func()) {
	graph := NewGraph()
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

	observerCh := make(chan DAGEvent, 128)
	graph.SetObserver(observerCh)
	a.dagMu.Lock()
	a.dagGraph = graph
	a.dagAlertID = trigger.AlertID
	a.dagSessionID = trigger.SessionID
	a.dagMu.Unlock()
	go a.dagFanOut(observerCh)
	a.broadcastDAGEvent(DAGEvent{Type: "start", AlertID: trigger.AlertID, SessionID: trigger.SessionID, Nodes: graph.Snapshot()})

	cleanup := func() {
		a.broadcastDAGEvent(DAGEvent{Type: "done", AlertID: trigger.AlertID, SessionID: trigger.SessionID, Nodes: graph.Snapshot()})
		a.dagMu.Lock()
		a.dagGraph = nil
		a.dagAlertID = ""
		a.dagSessionID = ""
		a.dagMu.Unlock()
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
 *            - Reflection/Interjection: parse decision (continue/conclude/investigate)
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
// scheduleOutcome holds the outcome of plan+schedule, including an optional verdict
// from reflection that can skip the aggregator.
type scheduleOutcome struct {
	Intent              gates.Intent
	ReflectionVerdict   string // non-empty if reflection concluded with a full verdict
	ReflectionAggregate *bool  // reflector's recommendation: true = needs aggregator, false = verdict is complete
}

func (a *Agent) runPlanAndSchedule(ctx context.Context, trigger Trigger, graph *Graph, budget *Budget) (*scheduleOutcome, error) {
	// Inject data directory override into context for retrieval tools (relay/gateway paths)
	if trigger.DataDir != "" {
		ctx = tools.WithDataDir(ctx, trigger.DataDir)
	}
	// Propagate session ID onto the graph so compute nodes can resolve
	// per-session interfaces.json without threading trigger through every layer.
	graph.SessionID = trigger.SessionID

	// Resolve DAG mode: trigger-level override (from frontend) > config default
	dagMode := a.cfg.DAGMode
	if trigger.DAGMode != "" {
		dagMode = trigger.DAGMode
	}

	// ── Phase 0: Preflight ──
	// One executor-model LLM call answers four questions at once:
	//   - skills: which guidance cards apply
	//   - mode: chat / meta / investigate (chat and meta short-circuit the executive)
	//   - intent: a rank from the intent registry (used when trigger intent is Auto)
	//   - required_categories: tool categories the plan must include
	if a.cfg.ClassifierEnabled {
		if !budget.TrySpawnNode("", true) {
			return nil, fmt.Errorf("budget exhausted before preflight")
		}
		a.preflight = a.preflightQuery(ctx, formatTrigger(trigger), trigger.History)
		// Per-investigation card list lives on the Graph (DAG path).
		// a.activeCards is also set so the legacy ReAct path keeps working
		// — ReAct doesn't use Graph the same way and is still serialized.
		graph.ActiveCards = a.preflight.Skills
		a.activeCards = a.preflight.Skills
		log.Printf("[dag] preflight: mode=%s intent=%s skills=%v categories=%v context=%q",
			a.preflight.Mode, a.preflight.Intent, a.preflight.Skills, a.preflight.RequiredCategories, Text.TruncateLog(a.preflight.Context, 120))

		// Autonomous mode: force investigate regardless of preflight classification.
		// The agent always acts, never chats. Per-request override wins over config.
		execMode := a.cfg.ExecutionMode
		if trigger.ExecutionMode != "" {
			execMode = trigger.ExecutionMode
		}
		if execMode == "autonomous" && (a.preflight.Mode == "chat" || a.preflight.Mode == "meta") {
			log.Printf("[dag] autonomous mode: overriding preflight mode=%s → investigate", a.preflight.Mode)
			a.preflight.Mode = "investigate"
		}

		// Short-circuit chat/meta queries — skip the executive entirely.
		// Only fires in interactive mode (autonomous overrides above).
		if a.preflight.Mode == "chat" || a.preflight.Mode == "meta" {
			log.Printf("[dag] preflight short-circuit: mode=%s, skipping planner", a.preflight.Mode)
			return nil, &ExecutiveConversationalError{Text: ""}
		}
	}

	// ── Phase 1: Planner ──
	if !budget.TrySpawnNode("", true) {
		return nil, fmt.Errorf("budget exhausted before planner")
	}
	a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: "executive", Node: &NodeInfo{ID: "executive", Type: "executive", State: "running", Tag: "plan"}})

	planResult, err := a.runExecutive(ctx, trigger, graph)
	if err != nil {
		// Conversational response (trivial query) — not a real failure
		var convErr *ExecutiveConversationalError
		if errors.As(err, &convErr) {
			a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: "executive", Node: &NodeInfo{ID: "executive", Type: "executive", State: "resolved", Tag: "direct answer"}})
			if convErr.Text != "" {
				a.broadcastDAGEvent(DAGEvent{Type: "verdict", Text: convErr.Text})
			}
			return nil, err
		}
		a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: "executive", Node: &NodeInfo{ID: "executive", Type: "executive", State: "failed", Tag: "plan", Error: err.Error()}})
		return nil, fmt.Errorf("planner failed: %w", err)
	}
	a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: "executive", Node: &NodeInfo{ID: "executive", Type: "executive", State: "resolved", Tag: "plan"}})

	initialNodes, err := planStepsToNodes(planResult.Steps, graph, budget, a.registry, dagMode)
	if err != nil {
		return nil, fmt.Errorf("plan-to-nodes failed: %w", err)
	}

	// Store capability gaps on the graph for reflection/aggregator context
	if len(planResult.Gaps) > 0 {
		graph.Gaps = planResult.Gaps
		log.Printf("[dag] capability gaps: %v", planResult.Gaps)
	}

	log.Printf("[dag] plan: %d nodes created", len(initialNodes))

	// ── Phase 2: Scheduler loop ──
	completionCh := make(chan nodeCompletion, 64)
	inflight := 0
	throttle := newToolThrottle()
	batchCounter := 0             // nodes resolved since last reflection (nReflect mode)
	reflectionConcluded := false  // true if a reflection already decided "conclude"
	reflectionVerdict := ""       // full verdict from reflection (skips aggregator if non-empty)
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
	launchReady := func() {
		for _, n := range graph.ReadyNodes() {
			graph.SetState(n.ID, StateRunning)
			n.StartedAt = time.Now()
			inflight++
			// Broadcast node running event
			a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: n.ID, Node: graph.SnapshotNode(n.ID)})
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
					MaxBudget: 8000,
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
				go a.fireNode(ctx, n, graph, budget, completionCh, trigger.AlertID, throttle, intent, trigger.Scope)
			}
		}
	}

	// injectInterjection checks for human messages and creates a gating reflection node.
	// Returns true if an interjection was injected (caller should not launchReady yet —
	// the reflection will complete and launchReady will fire then).
	injectInterjection := func() bool {
		if a.interjections == nil {
			return false
		}
		select {
		case msg := <-a.interjections:
			if !budget.TrySpawnNode("", true) {
				log.Printf("[dag] no LLM budget for interjection reflection, message lost: %s", Text.TruncateLog(msg, 100))
				return false
			}
			rNode := &Node{
				Type: NodeInterjection,
				Tag:  "operator",
			}
			rID := graph.AddNode(rNode)

			// Gate all pending nodes behind this reflection
			gated := graph.GatePending(rID)
			log.Printf("[dag] interjection node %s injected, gating %d pending nodes: %s",
				rID, gated, Text.TruncateLog(msg, 100))

			graph.SetState(rID, StateRunning)
			rNode.StartedAt = time.Now()
			inflight++
			a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: rID, Node: graph.SnapshotNode(rID)})
			// Build interjection context via ContextGate. Reflector is
			// deterministic — no Query, just node returns + worklog.
			intCtx, ictxErr := graph.Context.Get(ctx, ContextRequest{
				ReturnSources: Sources(
					NodeReturns("all"),
					Worklog(20, "all"),
				),
				MaxBudget: 8000,
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
		a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: rID, Node: graph.SnapshotNode(rID)})
		// Build batch reflection context via ContextGate. Deterministic, no curator.
		batchCtxResp, bctxErr := graph.Context.Get(ctx, ContextRequest{
			ReturnSources: Sources(
				NodeReturns("all"),
				Worklog(10, "all"),
			),
			MaxBudget: 8000,
		})
		if bctxErr != nil {
			log.Printf("[dag] batch reflection context build failed: %v", bctxErr)
			batchCtxResp = &ContextResponse{Sources: map[string]string{}}
		}
		go a.fireReflection(ctx, rNode, graph, budget, completionCh, trigger, batchCtxResp, intent)
		budget.ResetWaveCounters()
		batchCounter = 0
		log.Printf("[dag] batch reflection injected at %s", rID)
	}

	maxInvestigations := a.cfg.MaxInvestigations
	if maxInvestigations <= 0 {
		maxInvestigations = 1
	}
	investigationCount := 0
	// diminishingStreak tracks consecutive reflector passes that reported
	// progress=diminishing. Two in a row downgrades the current decision
	// from "investigate" to "conclude" so we don't spawn fresh Holmes
	// cycles when fixes aren't moving the needle.
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
			log.Printf("[dag] injecting reflection (%d tool completions since last reflect, investigation %d/%d)", workSinceReflection, investigationCount, maxInvestigations)
			rNode := &Node{
				Type:   NodeReflection,
				Tag:    "reflect",
				Params: map[string]any{"investigation_count": investigationCount},
			}
			rID := graph.AddNode(rNode)
			graph.SetState(rID, StateRunning)
			rNode.StartedAt = time.Now()
			inflight = 1
			reflectionInflight = true
			// Sync point: previous Holmes/microplanner cycle is provably done
			// by the time we get here (inflight has dropped to 0). Clear once.
			debuggerInflight = false
			a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: rID, Node: graph.SnapshotNode(rID)})
			// Build main reflection context via ContextGate. Deterministic, no curator.
			reflCtxResp, rctxErr := graph.Context.Get(ctx, ContextRequest{
				ReturnSources: Sources(
					NodeReturns("all"),
					Worklog(10, "all"),
				),
				MaxBudget: 8000,
			})
			if rctxErr != nil {
				log.Printf("[dag] reflection context build failed: %v", rctxErr)
				reflCtxResp = &ContextResponse{Sources: map[string]string{}}
			}
			go a.fireReflection(ctx, rNode, graph, budget, completionCh, trigger, reflCtxResp, intent)
		}

		select {
		case <-ctx.Done():
			log.Printf("[dag] wall clock expired, aborting %d inflight nodes", inflight)
			inflight = 0

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
			a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: node.ID, Node: graph.SnapshotNode(node.ID)})

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
						if len(obs.Nodes) > 0 {
							newNodes, graftErr := planStepsToNodes(obs.Nodes, graph, budget, a.registry, dagMode)
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
							a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: rID, Node: graph.SnapshotNode(rID)})
							obsCtxResp, octxErr := graph.Context.Get(ctx, ContextRequest{
								ReturnSources: Sources(
									NodeReturns("all"),
									Worklog(10, "all"),
								),
								MaxBudget: 8000,
							})
							if octxErr != nil {
								log.Printf("[dag] observer reflection context build failed: %v", octxErr)
								obsCtxResp = &ContextResponse{Sources: map[string]string{}}
							}
							go a.fireReflection(ctx, rNode, graph, budget, completionCh, trigger, obsCtxResp, intent)
							budget.ResetWaveCounters()
							log.Printf("[dag] observer triggered reflection (%s)", obs.Reason)
						}
					}
				}
				launchReady()
				continue
			}

			if comp.Err != nil {
				errMsg := comp.Err.Error()
				log.Printf("[dag] node %s (%s) failed: %v", comp.NodeID, node.ToolName, comp.Err)
				// debuggerInflight is cleared by the next reflection that
				// fires (Fix 4 — reflector is the sync point), so no need
				// to clear it here on individual completion failures.

				// ── Three-tier retry ──
				// Tier 1: skip — structural error, no retry will help
				// Tier 2: blind — transient error, rerun same command
				// Tier 3: oneshot — executor LLM fix with tiny context
				tier := classifyRetryTier(errMsg)
				alreadyRetried := strings.Contains(node.Tag, "[")

				// Holmes-spawned action nodes are exempt from retry. Holmes
				// is investigating; failure observations are signal, not bugs to
				// fix. Rewriting the command would invalidate the experiment.
				if tier != "skip" && !alreadyRetried && node.Type == NodeTool && node.ToolName != "service" && node.Source != "holmes" {
					if tier == "blind" {
						node.Tag = node.Tag + " [blind_retry]"
						node.Error = nil
						graph.SetState(comp.NodeID, StatePending)
						log.Printf("[dag] blind retry for %s", comp.NodeID)
						appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "BLIND_RETRY", errMsg)
						a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: comp.NodeID, Node: graph.SnapshotNode(comp.NodeID)})
						launchReady()
						continue
					}
					if tier == "oneshot" && budget.LLMRemaining() > 0 {
						// Fire a tiny executor call: just command + error → fixed command.
						// Keep node in failed state so dependents can proceed.
						// Track inflight so scheduler doesn't exit early.
						// If retry succeeds, resolve the node and dependents will
						// see the result on the next launchReady cycle.
						graph.SetError(comp.NodeID, comp.Err)
						node.Tag = node.Tag + " [oneshot_retry]"
						inflight++ // track the async retry
						go a.oneshotRetry(ctx, node, comp, graph, budget, completionCh, errMsg, intent, trigger.Scope)
						log.Printf("[dag] oneshot retry for %s", comp.NodeID)
						appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "ONESHOT_RETRY", errMsg)
						a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: comp.NodeID, Node: graph.SnapshotNode(comp.NodeID)})
						launchReady() // let dependents proceed with failed dep
						continue
					}
				}

				// No retry — fail and prune
				graph.SetError(comp.NodeID, comp.Err)
				if strings.Contains(errMsg, "gate:") {
					appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "GATE_BLOCKED", errMsg)
				} else {
					appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "FAILED", errMsg)
				}
				// Don't cascade prune — let downstream nodes attempt to run.
				// The reflector will see the failure and decide what to do.
				launchReady()
				continue

			} else if node.Type == NodeHolmes {
				// Holmes investigation iteration completed.
				// Three possible next steps:
				//   1. conclude=true   → dispatch microplanner with the RCA
				//   2. actions present → graft all as parallel tool nodes + queue
				//                        the next Holmes iteration depending on all
				//   3. iter cap hit    → force conclude with low confidence
				graph.SetResult(comp.NodeID, comp.Result)
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
					a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: an.ID, Node: graph.SnapshotNode(an.ID)})
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
				a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: nextNode.ID, Node: graph.SnapshotNode(nextNode.ID)})
				log.Printf("[dag] holmes iter %d → %d actions → iter %d queued",
					prevState.Iter, len(actionNodes), prevState.Iter+1)
				launchReady()

			} else if node.Type == NodeMicroPlanner {
				// Clean-room debugger completed — parse plan and graft steps
				graph.SetResult(comp.NodeID, comp.Result)

				var mpOutput microPlannerOutput
				if err := ParseLLMJSON(comp.Result, &mpOutput); err != nil {
					log.Printf("[dag] debugger parse failed: %v", err)
				} else if len(mpOutput.Nodes) > 0 {
					log.Printf("[dag] debugger diagnosis: %s (%d steps)", Text.TruncateLog(mpOutput.Summary, 200), len(mpOutput.Nodes))
					appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "DEBUG_PLAN", fmt.Sprintf("%d steps: %s", len(mpOutput.Nodes), Text.TruncateLog(mpOutput.Summary, 150)))

					newNodes, graftErr := planStepsToNodes(mpOutput.Nodes, graph, budget, a.registry, dagMode)
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
								a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: nn.ID, Node: graph.SnapshotNode(nn.ID)})
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
								a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: vID, Node: graph.SnapshotNode(vID)})
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
					// waves downgrade investigate→conclude so Holmes cycles
					// stop spawning when fixes aren't moving the validator
					// set. Empty / unknown / "productive" resets the streak.
					switch ref.Progress {
					case "diminishing":
						diminishingStreak++
						log.Printf("[reflector:diminishing] streak=%d (decision=%q): %s", diminishingStreak, ref.Decision, Text.TruncateLog(ref.Summary, 160))
						appendWorklog(a.cfg.MetadataDir, graph.SessionID, "reflect", "DIMINISHING", fmt.Sprintf("streak=%d | %s", diminishingStreak, Text.TruncateLog(ref.Summary, 180)))
						if diminishingStreak >= 2 && ref.Decision == "investigate" {
							log.Printf("[reflector:diminishing] streak hit 2 — downgrading investigate→conclude")
							ref.Decision = "conclude"
							if ref.Aggregate == nil {
								t := true
								ref.Aggregate = &t
							}
							if ref.Verdict == "" {
								ref.Verdict = ref.Summary
							}
						}
					default:
						diminishingStreak = 0
					}

					switch ref.Decision {
					case "continue":
						graph.SetResult(comp.NodeID, comp.Result)
						budget.ResetWaveCounters()
						investigationCount = 0 // reset — if previous investigation worked, next reflection starts fresh
						log.Printf("[dag] reflection: continue (%s), wave counters reset", ref.Reason)
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

					case "conclude":
						graph.SetResult(comp.NodeID, ref.Verdict)
						graph.SkipAllPending()
						reflectionConcluded = true
						reflectionVerdict = ref.Verdict
						reflectionAggregate = ref.Aggregate
						log.Printf("[dag] reflection: conclude early (%s)", ref.Reason)
						appendWorklog(a.cfg.MetadataDir, graph.SessionID, "reflect", "CONCLUDE", Text.TruncateLog(ref.Reason, 200))

					case "investigate":
						investigationCount++
						graph.SetResult(comp.NodeID, comp.Result)
						skipped := graph.SkipAllPending()
						budget.ResetWaveCounters()

						// Use Summary with Reason as fallback
						summary := ref.Summary
						if summary == "" {
							summary = ref.Reason
						}
						problem := ref.Problem
						if problem == "" {
							problem = summary
						}

						log.Printf("[dag] reflection: investigate #%d (%s), skipped %d pending", investigationCount, summary, skipped)
						appendWorklog(a.cfg.MetadataDir, graph.SessionID, "reflect", "INVESTIGATE", fmt.Sprintf("#%d, skipped %d pending | %s", investigationCount, skipped, Text.TruncateLog(summary, 200)))

						// Holmes fires on EVERY investigation regardless of plan
						// shape. The old gate skipped non-compute investigations
						// because the prior microplanner would invent fictional
						// code fixes. Holmes is read-only — it can investigate
						// any failure type (ops, research, code) without risk.
						// If the resulting RCA isn't actionable by the microplanner
						// (e.g., a research query whose root cause is "the source
						// site is down"), the microplanner will produce a no-op
						// plan and the next reflection cycle concludes naturally.

						// Dispatch to Holmes — the ReAct investigator phase. Holmes
						// gathers evidence with read-only tools, forms hypotheses, and
						// emits a structured RCA. The microplanner only fires after
						// Holmes concludes (handled in the NodeHolmes completion
						// branch below). This is the "investigate before fixing" rule:
						// every investigate decision goes through diagnosis first, no shortcuts.
						if budget.TrySpawnNode("", true) {
							// Snapshot currently-failed node IDs so we can mark them
							// superseded once the eventual fix succeeds. We carry this
							// forward through Holmes to the microplanner via the
							// addressingFailures map keyed by investigationCount.
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
								log.Printf("[dag] holmes setup failed: %v", err)
							} else {
								inflight++
								a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: sNode.ID, Node: graph.SnapshotNode(sNode.ID)})
								go a.fireHolmes(ctx, sNode, graph, budget, completionCh, trigger, intent)
								debuggerInflight = true
								log.Printf("[dag] dispatched holmes %s (iter 1/%d): %s", sNode.ID, maxIter, Text.TruncateLog(problem, 200))
							}
						} else {
							log.Printf("[dag] no budget for holmes, investigation stalled")
						}
					}
				}

			} else {
				// Tool/compute node resolved successfully
				graph.SetResult(comp.NodeID, comp.Result)

				// ── Detect bash errors (non-zero exit returned as result, not error) ──
				if bashErr, isBash := isBashError(comp.Result); isBash && node.Type == NodeTool && node.ToolName == "bash" {
					log.Printf("[dag] node %s (bash) completed with error: %s", comp.NodeID, Text.TruncateLog(comp.Result, 500))
					graph.SetError(comp.NodeID, bashErr)
					node.Error = bashErr
					if strings.HasPrefix(node.Tag, "verify_") || strings.HasPrefix(node.Tag, "revalidate_") {
						appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "VALIDATION_FAIL", comp.Result)
					} else {
						appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "BASH_ERROR", comp.Result)
					}

					// No cascade prune — the reflector will catch this
					injectInterjection()
					launchReady()
					continue
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
						appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "OK", fmt.Sprintf("%s: %s", node.ToolName, Text.TruncateLog(comp.Result, 100)))
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
							merged, err := mergeJSONField(parent.Result, "output", stdout)
							if err == nil {
								parent.Result = merged
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
					if json.Unmarshal([]byte(comp.Result), &svcResult) == nil && svcResult.Status == "started" {
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
							a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: hID, Node: graph.SnapshotNode(hID)})
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
						Services []struct {
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
					unmarshalErr := json.Unmarshal([]byte(comp.Result), &cr)
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
							a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: sID, Node: graph.SnapshotNode(sID)})
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
							a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: fID, Node: graph.SnapshotNode(fID)})
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
										execNode := &Node{
											Type:      NodeTool,
											ToolName:  "bash",
											Params:    map[string]any{"command": execCmd},
											DependsOn: append([]string{}, allCoderIDs...),
											SpawnedBy: comp.NodeID,
											Tag:       computeNodes[i].Tag + "_exec",
											Source:    "builtin",
										}
										eID := graph.AddNode(execNode)
										allGraftedNodes = append(allGraftedNodes, execNode)
										graph.AddChild(comp.NodeID, eID)
										a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: eID, Node: graph.SnapshotNode(eID)})
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
										a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: sID, Node: graph.SnapshotNode(sID)})
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
							a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: sID, Node: graph.SnapshotNode(sID)})
							log.Printf("[dag] compute plan → grafted top-level service node %s: %s", sID, svc.Command)
						}

						// Phase 4: validation wave — architect-declared checks.
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
								a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: vID, Node: graph.SnapshotNode(vID)})
							}
							if len(validationNodes) > 0 {
								log.Printf("[dag] compute plan → grafted %d validation checks", len(validationNodes))
							}
						} else {
							log.Printf("[dag] compute plan emitted no validation — no validation wave grafted")
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
							Execute    string `json:"execute,omitempty"`
							Validation string `json:"validation,omitempty"`
							Type       string `json:"type,omitempty"`
						}
						if json.Unmarshal([]byte(comp.Result), &res) == nil && res.Type == "result" && res.Execute != "" {
							grafted := a.graftComputeExecution(graph, node, comp.NodeID, res.Execute, res.Validation, budget)
							if len(grafted) > 0 {
								rewriteDependentsMultiExcluding(graph, comp.NodeID, grafted)
								log.Printf("[dag] shallow compute → grafted %d exec/verify nodes", len(grafted))
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
		ReflectionVerdict:   reflectionVerdict,
		ReflectionAggregate: reflectionAggregate,
	}, nil
}

/*
 * runDAG executes the optimistic DAG investigation pipeline.
 * desc: Runs the full pipeline: setup, plan+schedule, aggregate, execute
 *       actuator actions, and record the investigation in the event store.
 *       Called for async (fire-and-forget) investigations.
 * param: ctx - context for the investigation.
 * param: trigger - the investigation trigger.
 */
func (a *Agent) runDAG(ctx context.Context, trigger Trigger) {
	log.Printf("[dag] starting investigation: type=%s alert=%s source=%s",
		trigger.Type, trigger.AlertID, trigger.Source)

	startTime := time.Now()
	graph, budget, cleanup := a.setupDAGPipeline(trigger)
	defer cleanup()

	// No hard wall clock — the kernel's heartbeat module manages soft timeouts.
	dagCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	pr, err := a.runPlanAndSchedule(dagCtx, trigger, graph, budget)
	if err != nil {
		log.Printf("[dag] %v", err)
		return
	}
	resolvedIntent := pr.Intent

	// ── Phase 3: Aggregator ──
	var verdict string
	var actions []ActuatorAction
	if dagCtx.Err() != nil {
		log.Printf("[dag] skipping aggregator — context cancelled")
		return
	}
	if !budget.TrySpawnNode("", true) {
		log.Printf("[dag] budget exhausted before aggregator")
		return
	}
	var aggErr error
	aggCtxResp, actxErr := graph.Context.Get(dagCtx, ContextRequest{
		ReturnSources: Sources(
			NodeReturns("all"),
			Worklog(30, "all"),
		),
		MaxBudget: 12000,
	})
	if actxErr != nil {
		log.Printf("[dag] aggregator context build failed: %v", actxErr)
		aggCtxResp = &ContextResponse{Sources: map[string]string{}}
	}
	verdict, actions, aggErr = a.runAggregatorWithIntent(dagCtx, trigger, graph, resolvedIntent, trigger.History, aggCtxResp)
	if aggErr != nil {
		log.Printf("[dag] aggregator failed: %v", aggErr)
		return
	}

	log.Printf("[dag] verdict: %s", Text.TruncateLog(verdict, 500))

	// ── Phase 4: Execute actuator actions ──
	for _, action := range actions {
		if dagCtx.Err() != nil {
			break
		}
		result, execErr := a.executeToolNode(dagCtx, nil, nil, nil, action.Tool, action.Params, trigger.AlertID, resolvedIntent, trigger.Scope)
		if execErr != nil {
			log.Printf("[dag] actuator %s failed: %v", action.Tool, execErr)
		} else {
			log.Printf("[dag] actuator %s: %s", action.Tool, Text.TruncateLog(result, 500))
		}
	}

	elapsed := time.Since(startTime)
	log.Printf("[dag] investigation complete in %s (alert=%s, nodes=%d, llm=%d)",
		elapsed.Round(time.Millisecond), trigger.AlertID, graph.NodeCount(), budget.LLMCount())

	// Record investigation in event store
	if a.eventStore != nil {
		mode := a.cfg.DAGMode
		if trigger.DAGMode != "" {
			mode = trigger.DAGMode
		}
		refCount, investigationCount := graph.ReflectionStats()
		a.eventStore.InsertInvestigation(store.Investigation{
			ID:              trigger.AlertID,
			NodeID:          a.cfg.NodeID,
			TriggerType:     trigger.Type,
			TriggerAlertID:  trigger.AlertID,
			StartedAt:       startTime.Unix(),
			CompletedAt:     time.Now().Unix(),
			DurationMs:      elapsed.Milliseconds(),
			Intent:          resolvedIntent.String(),
			DAGMode:         mode,
			NodesCount:      graph.NodeCount(),
			LLMCalls:        int(budget.LLMCount()),
			ReflectionCount: refCount,
			ReplanCount:     investigationCount, // legacy field name; persisted as `replan_count` in the audit DB schema. Same semantic as investigation count.
			Verdict:         verdict,
			Status:          "completed",
		})
	}
}

/*
 * SyncResult contains the full output from a synchronous DAG investigation.
 * desc: Wraps the synthesized verdict, recommended follow-up actions, and
 *       any capability gaps declared by the planner.
 */
type SyncResult struct {
	Verdict  string           // synthesized response text
	Actions  []ActuatorAction // recommended follow-up actions (caller decides whether to execute)
	Gaps     []string         // capability gaps declared by the planner
	Nodes    int              // total DAG nodes executed
	LLMCalls int              // total LLM round-trips
}

/*
 * RunDAGSync runs the full DAG pipeline synchronously and returns the result.
 * desc: Used by the API to route queries through the parallel investigation
 *       engine. Actions are returned to the caller — not auto-executed.
 *       Handles ExecutiveConversationalError by falling back to direct LLM chat
 *       or returning the planner's conversational text.
 * param: ctx - context for the investigation.
 * param: trigger - the investigation trigger.
 * return: SyncResult pointer with verdict, actions, and gaps, or error.
 */
func (a *Agent) RunDAGSync(ctx context.Context, trigger Trigger) (*SyncResult, error) {
	// Route to ReAct loop if mode=react
	if trigger.DAGMode == "react" {
		return a.RunReActSync(ctx, trigger)
	}

	log.Printf("[dag] sync investigation: type=%s alert=%s source=%s",
		trigger.Type, trigger.AlertID, trigger.Source)

	// Mark the start of a new run in the worklog. History is preserved so
	// Holmes can see prior failures, but the separator lets the reflector
	// distinguish current vs stale evidence.
	markRunStart(a.cfg.MetadataDir, trigger.SessionID)
	rotateServiceLogs(a.cfg.Workspace)

	a.investigating.Store(true)
	defer func() {
		a.investigating.Store(false)
		// Drain any pending interjections
		for {
			select {
			case <-a.interjections:
			default:
				return
			}
		}
	}()

	startTime := time.Now()
	graph, budget, cleanup := a.setupDAGPipeline(trigger)
	defer cleanup()

	// No hard wall clock — the kernel's heartbeat module manages soft timeouts.
	dagCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	pr, err := a.runPlanAndSchedule(dagCtx, trigger, graph, budget)
	if err != nil {
		// If planner returned conversational text, surface it as the verdict
		// instead of failing the whole pipeline. This handles vague queries.
		var convErr *ExecutiveConversationalError
		if errors.As(err, &convErr) {
			if convErr.Text != "" {
				return &SyncResult{Verdict: convErr.Text}, nil
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
			if query != "" {
				chatPrompt := a.soulPrompt
				if graph != nil && graph.Context != nil {
					gateResp, gerr := graph.Context.Get(dagCtx, ContextRequest{
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
				chatPrompt += "\n\nIMPORTANT: You are in chat-only mode — you cannot execute tools, run commands, or build anything right now. Never say \"I'll do X\" or \"Let me build X\" because you can't follow through. If the user is asking you to do something, answer their question or give information, but do not promise actions you cannot take."
				chatPrompt += "\n\n## Output format\n" + a.FormatRule()
				resp, llmErr := a.llm.Complete(dagCtx, &llm.ChatRequest{
					Messages:    BuildMessagesWithHistory(chatPrompt, query, trigger.History),
					Temperature: a.cfg.Temperature,
					MaxTokens:   a.cfg.MaxTokens,
				})
				if llmErr == nil && len(resp.Choices) > 0 {
					return &SyncResult{Verdict: resp.Choices[0].Message.Content}, nil
				}
			}
			return &SyncResult{Verdict: "I'm not sure how to help with that. Could you be more specific?"}, nil
		}
		return nil, err
	}
	resolvedIntent := pr.Intent

	// Aggregator: agg_mode -1=auto (reflector decides), 0=skip, 1=executor model, 2=reasoning model
	aggMode := trigger.AggMode
	var verdict string
	var actions []ActuatorAction

	// Auto mode: let the reflector decide — but always aggregate when compute nodes are involved
	hasCompute := graph.HasNodeOfType(NodeCompute)
	if aggMode == -1 && pr.ReflectionVerdict != "" {
		if hasCompute {
			aggMode = 1 // compute runs always need the aggregator for a proper formatted response
			log.Printf("[dag] auto agg: compute nodes present, forcing aggregator")
		} else if pr.ReflectionAggregate != nil && *pr.ReflectionAggregate {
			aggMode = 2 // reflector says aggregate needed — use reasoning model
			log.Printf("[dag] auto agg: reflector requested aggregation (reasoning model)")
		} else {
			aggMode = 0 // reflector says verdict is complete — skip
			log.Printf("[dag] auto agg: reflector verdict is complete, skipping aggregator")
		}
	}

	// If reflection concluded AND aggregator is disabled, use reflection verdict directly
	if pr.ReflectionVerdict != "" && aggMode == 0 {
		log.Printf("[dag] skipping aggregator (agg_mode=0, reflection concluded)")
		verdict = pr.ReflectionVerdict
		a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: "aggregator", Node: &NodeInfo{ID: "aggregator", Type: "aggregator", State: "resolved", Tag: "synthesize (skipped)"}})
		a.broadcastDAGEvent(DAGEvent{Type: "verdict", Text: verdict})
	} else {
		if dagCtx.Err() != nil {
			return nil, fmt.Errorf("wall clock expired before aggregator")
		}
		// Aggregator is exempt from budget — it must always run to give the user a response
		budget.TrySpawnNode("", true) // charge if possible, but don't block

		// Select LLM client based on agg_mode
		aggClient := a.llm // default: reasoning model
		aggLabel := "reasoning"
		if aggMode == 1 {
			aggClient = a.executor // executor model only when explicitly requested
			aggLabel = "executor"
		}
		log.Printf("[dag] aggregator using %s model (agg_mode=%d)", aggLabel, aggMode)

		a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: "aggregator", Node: &NodeInfo{ID: "aggregator", Type: "aggregator", State: "running", Tag: "synthesize"}})

		var aggErr error
		aggCtxResp2, actxErr2 := graph.Context.Get(dagCtx, ContextRequest{
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
		verdict, actions, aggErr = a.runAggregatorWithClient(dagCtx, trigger, graph, resolvedIntent, trigger.History, aggClient, aggCtxResp2)
		if aggErr != nil {
			a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: "aggregator", Node: &NodeInfo{ID: "aggregator", Type: "aggregator", State: "failed", Tag: "synthesize", Error: aggErr.Error()}})
			return nil, fmt.Errorf("aggregator failed: %w", aggErr)
		}
		a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: "aggregator", Node: &NodeInfo{ID: "aggregator", Type: "aggregator", State: "resolved", Tag: "synthesize"}})
	}

	elapsed := time.Since(startTime)
	log.Printf("[dag] sync investigation complete in %s (alert=%s, nodes=%d, llm=%d)",
		elapsed.Round(time.Millisecond), trigger.AlertID, graph.NodeCount(), budget.LLMCount())

	// Record investigation in event store
	if a.eventStore != nil {
		mode := a.cfg.DAGMode
		if trigger.DAGMode != "" {
			mode = trigger.DAGMode
		}
		refCount, investigationCount := graph.ReflectionStats()
		a.eventStore.InsertInvestigation(store.Investigation{
			ID:              trigger.AlertID,
			NodeID:          a.cfg.NodeID,
			TriggerType:     trigger.Type,
			TriggerAlertID:  trigger.AlertID,
			StartedAt:       startTime.Unix(),
			CompletedAt:     time.Now().Unix(),
			DurationMs:      elapsed.Milliseconds(),
			Intent:          resolvedIntent.String(),
			DAGMode:         mode,
			NodesCount:      graph.NodeCount(),
			LLMCalls:        int(budget.LLMCount()),
			ReflectionCount: refCount,
			ReplanCount:     investigationCount, // legacy field name; persisted as `replan_count` in the audit DB schema. Same semantic.
			Verdict:         verdict,
			Status:          "completed",
		})
	}

	return &SyncResult{
		Verdict:  verdict,
		Actions:  actions,
		Gaps:     graph.Gaps,
		Nodes:    graph.NodeCount(),
		LLMCalls: int(budget.LLMCount()),
	}, nil
}

/*
 * isBashError checks if a resolved bash result contains a structured error.
 * desc: Returns the error and true if the result has bash_error:true.
 */
func isBashError(result string) (error, bool) {
	if !strings.Contains(result, `"bash_error":true`) {
		return nil, false
	}
	return fmt.Errorf("bash failed: %s", Text.TruncateLog(result, 300)), true
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
 * result so downstream param_refs can reach it).
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

// classifyRetryTier determines what kind of retry (if any) is appropriate.
// Returns "skip" (no retry), "blind" (rerun same command), or "oneshot" (LLM fix).
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
	}
	for _, p := range skipPatterns {
		if strings.Contains(lower, p) {
			return "skip"
		}
	}

	// Tier 2: blind — transient errors, just rerun
	blindPatterns := []string{
		"connection refused", "econnrefused", "econnreset",
		"etimedout", // network timeout (not command timeout)
		"exit status 7", // curl: couldn't connect
		"npm err! network", "fetch failed",
		"rate limit", "http 429", "http 503",
	}
	for _, p := range blindPatterns {
		if strings.Contains(lower, p) {
			return "blind"
		}
	}

	// Tier 3: oneshot — everything else gets a cheap LLM fix attempt
	return "oneshot"
}

// oneshotRetry fires a tiny executor LLM call to fix a failed command.
// Context: just the command + error. No worklog, no blueprint, no evidence.
// The LLM returns a fixed command which is gate-checked and re-executed.
func (a *Agent) oneshotRetry(ctx context.Context, node *Node, comp nodeCompletion,
	graph *Graph, budget *Budget, ch chan nodeCompletion, errMsg string,
	intent gates.Intent, scope *ResolvedScope) {

	command, _ := node.Params["command"].(string)
	if command == "" {
		ch <- nodeCompletion{NodeID: comp.NodeID, Err: comp.Err}
		return
	}

	prompt := fmt.Sprintf("Command failed:\n%s\n\nError:\n%s\n\nReturn ONLY the fixed command, nothing else.", command, Text.TruncateLog(errMsg, 300))

	resp, err := a.executor.Complete(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "Fix the shell command based on the error. Return ONLY the corrected command, no explanation."},
			{Role: "user", Content: prompt},
		},
		Temperature: 0.0,
		MaxTokens:   256,
	})
	if err != nil || len(resp.Choices) == 0 {
		ch <- nodeCompletion{NodeID: comp.NodeID, Err: comp.Err}
		return
	}

	fixed := strings.TrimSpace(resp.Choices[0].Message.Content)
	fixed = strings.Trim(fixed, "`")
	if strings.HasPrefix(fixed, "```") {
		lines := strings.SplitN(fixed, "\n", 2)
		if len(lines) > 1 {
			fixed = strings.TrimSuffix(strings.TrimSpace(lines[1]), "```")
		}
	}

	if fixed == "" || fixed == command {
		ch <- nodeCompletion{NodeID: comp.NodeID, Err: comp.Err}
		return
	}

	log.Printf("[dag] oneshot fix: %q → %q", Text.TruncateLog(command, 80), Text.TruncateLog(fixed, 80))

	// Gate-check the fixed command through normal IGX path
	node.Params["command"] = fixed
	fixedImpact := a.intentRegistry.ResolveToolIntent("bash", nil, node.Params)
	scopeCap := -1
	if scope != nil {
		if cap, ok := scope.MaxImpact["bash"]; ok {
			scopeCap = cap
		}
	}
	if gateErr := a.gate.CheckTriadWithScope(intent, "bash", fixedImpact, scopeCap); gateErr != nil {
		log.Printf("[dag] oneshot fix blocked by gate: %v", gateErr)
		node.Params["command"] = command // restore original
		ch <- nodeCompletion{NodeID: comp.NodeID, Err: comp.Err}
		return
	}

	appendWorklog(a.cfg.MetadataDir, graph.SessionID, node.Tag, "ONESHOT_FIX", fmt.Sprintf("%s → %s", Text.TruncateLog(command, 60), Text.TruncateLog(fixed, 60)))

	// Execute through the registry (same as normal dispatch)
	sk, ok := a.registry.Get("bash")
	if !ok {
		ch <- nodeCompletion{NodeID: comp.NodeID, Err: fmt.Errorf("bash tool not found")}
		return
	}
	result, execErr := sk.(interface {
		Execute(context.Context, map[string]any) (string, error)
	}).Execute(ctx, node.Params)
	if execErr != nil {
		ch <- nodeCompletion{NodeID: comp.NodeID, Err: execErr}
	} else {
		ch <- nodeCompletion{NodeID: comp.NodeID, Result: result}
	}
}

// graftComputeExecution wires an execute bash node (running the generated
// code) and a verify bash node (running a content check) downstream of a
// shallow-mode top-level compute node. Returns the grafted nodes so the
// caller can rewire downstream deps.
//
// The execute node tees its combined output to /tmp/kaiju_<node>.out; the
// verify command gets that path as $OUT. If the coder did not emit an
// explicit validation expression, we fall back to a generic check: output
// non-empty, no bare Python/JS traceback, minimum size.
func (a *Agent) graftComputeExecution(graph *Graph, comp *Node, compID, execCmd, validation string, budget *Budget) []*Node {
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
	grafted = append(grafted, execNode)
	a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: execID, Node: graph.SnapshotNode(execID)})

	check := strings.TrimSpace(validation)
	if check == "" {
		check = fmt.Sprintf(
			"[ -s $OUT ] && ! grep -qE '^(Traceback|Error:|SyntaxError)' $OUT && [ $(wc -c < $OUT) -gt 40 ]")
	}
	// Expose $OUT to the validator regardless of whether it was coder-declared.
	wrappedCheck := fmt.Sprintf("OUT=%s; %s", outFile, check)

	if !budget.TrySpawnNode("bash", false) {
		log.Printf("[dag] shallow compute → budget exhausted, skipping verify graft for %s", comp.ID)
		return grafted
	}
	verifyTag := "verify_" + sanitizeTag(comp.Tag)
	if verifyTag == "verify_" {
		verifyTag = "verify_" + comp.ID
	}
	verifyNode := &Node{
		Type:      NodeTool,
		ToolName:  "bash",
		Params:    map[string]any{"command": wrappedCheck, "timeout_sec": 30},
		DependsOn: []string{execID},
		SpawnedBy: compID,
		Tag:       verifyTag,
		Source:    "builtin",
	}
	vID := graph.AddNode(verifyNode)
	graph.AddChild(compID, vID)
	grafted = append(grafted, verifyNode)
	a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: vID, Node: graph.SnapshotNode(vID)})

	return grafted
}

// Dispatcher functions (fireNode, resolveInjections, extractJSONField,
// executeToolNode, toolThrottle) are in dispatcher.go.
