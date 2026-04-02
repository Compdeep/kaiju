package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/user/kaiju/internal/agent/gates"
	"github.com/user/kaiju/internal/agent/llm"
	"github.com/user/kaiju/internal/agent/tools"
	"github.com/user/kaiju/internal/compat/store"
)

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

	observerCh := make(chan DAGEvent, 128)
	graph.SetObserver(observerCh)
	a.dagMu.Lock()
	a.dagGraph = graph
	a.dagAlertID = trigger.AlertID
	a.dagMu.Unlock()
	go a.dagFanOut(observerCh)
	a.broadcastDAGEvent(DAGEvent{Type: "start", AlertID: trigger.AlertID, Nodes: graph.Snapshot()})

	cleanup := func() {
		a.broadcastDAGEvent(DAGEvent{Type: "done", AlertID: trigger.AlertID, Nodes: graph.Snapshot()})
		a.dagMu.Lock()
		a.dagGraph = nil
		a.dagAlertID = ""
		a.dagMu.Unlock()
		close(observerCh)
	}

	return graph, budget, cleanup
}

/*
 * runPlanAndSchedule runs phases 1-2: planner then scheduler loop.
 * desc: Returns nil error if all nodes complete (even if some failed/skipped).
 *
 *       The scheduler loop is mode-aware but structurally identical:
 *         1. Launch all ready nodes (deps satisfied)
 *         2. Wait for a completion
 *         3. Handle the completion based on node type:
 *            - Tool: record result, then mode-specific post-processing
 *            - Reflection/Interjection: parse decision (continue/conclude/replan)
 *            - Observer: parse action (continue/inject/cancel/reflect)
 *            - MicroPlanner: graft replacement nodes for failed tools
 *         4. Check for human interjection (all modes)
 *         5. Launch newly ready nodes
 *         6. Repeat until no inflight nodes remain
 *
 *       Mode-specific behavior happens at step 3 only:
 *         - reflect: reflections are structural (injected by planner, gate downstream)
 *         - nReflect: reflection injected every BatchSize tool completions
 *         - orchestrator: per-node orchestrator LLM spawned after each tool completes
 *
 *       Returns the resolved intent (which may differ from trigger.Intent() when
 *       the planner auto-infers it). A pre-aggregator reflection always fires at
 *       the end to evaluate results before aggregation.
 * param: ctx - context for the investigation (with wall clock timeout).
 * param: trigger - the investigation trigger.
 * param: graph - the investigation graph.
 * param: budget - the execution budget.
 * return: resolved IBE intent and error.
 */
// scheduleOutcome holds the outcome of plan+schedule, including an optional verdict
// from reflection that can skip the aggregator.
type scheduleOutcome struct {
	Intent             gates.Intent
	ReflectionVerdict  string // non-empty if reflection concluded with a full verdict
	ReflectionAggregate *bool // reflector's recommendation: true = needs aggregator, false = verdict is complete
}

func (a *Agent) runPlanAndSchedule(ctx context.Context, trigger Trigger, graph *Graph, budget *Budget) (*scheduleOutcome, error) {
	// Inject data directory override into context for retrieval tools (relay/gateway paths)
	if trigger.DataDir != "" {
		ctx = tools.WithDataDir(ctx, trigger.DataDir)
	}

	// Resolve DAG mode: trigger-level override (from frontend) > config default
	dagMode := a.cfg.DAGMode
	if trigger.DAGMode != "" {
		dagMode = trigger.DAGMode
	}

	// ── Phase 0: Classify capabilities (optional — disabled by default) ──
	if a.cfg.ClassifierEnabled && len(a.capabilities) > 0 {
		if !budget.TrySpawnNode("", true) {
			return nil, fmt.Errorf("budget exhausted before classifier")
		}
		a.activeCards = a.classifyCapabilities(ctx, formatTrigger(trigger))
		log.Printf("[dag] classified: %v", a.activeCards)
	}

	// ── Phase 1: Planner ──
	if !budget.TrySpawnNode("", true) {
		return nil, fmt.Errorf("budget exhausted before planner")
	}
	a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: "planner", Node: &NodeInfo{ID: "planner", Type: "planner", State: "running", Tag: "plan"}})

	planResult, err := a.runPlanner(ctx, trigger)
	if err != nil {
		// Conversational response (trivial query) — not a real failure
		var convErr *PlannerConversationalError
		if errors.As(err, &convErr) {
			a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: "planner", Node: &NodeInfo{ID: "planner", Type: "planner", State: "resolved", Tag: "direct answer"}})
			if convErr.Text != "" {
				a.broadcastDAGEvent(DAGEvent{Type: "verdict", Text: convErr.Text})
			}
			return nil, err
		}
		a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: "planner", Node: &NodeInfo{ID: "planner", Type: "planner", State: "failed", Tag: "plan", Error: err.Error()}})
		return nil, fmt.Errorf("planner failed: %w", err)
	}
	a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: "planner", Node: &NodeInfo{ID: "planner", Type: "planner", State: "resolved", Tag: "plan"}})

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
	batchCounter := 0           // nodes resolved since last reflection (nReflect mode)
	reflectionConcluded := false  // true if a reflection already decided "conclude"
	reflectionVerdict := ""       // full verdict from reflection (skips aggregator if non-empty)
	var reflectionAggregate *bool // reflector's recommendation on aggregation
	reflectionInflight := false  // true while a reflection node is running — prevents double injection
	workSinceReflection := 0    // tool nodes completed since last reflection — used to ensure final reflect

	// IBE: resolve intent — use planner-inferred intent for auto, else structural
	intent := trigger.Intent()
	if planResult.WasAuto {
		intent = planResult.InferredIntent
		// Cap inferred intent by clearance (planner can't escalate beyond node's ceiling)
		clr := gates.Intent(a.clearance.Clearance())
		if intent > clr {
			log.Printf("[dag] planner inferred %s but clearance is %d, capping", intent, clr)
			intent = clr
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
				reflectionInflight = true
				go a.fireReflection(ctx, n, graph, budget, completionCh, trigger, intent)
			} else {
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
			go a.fireInterjectionReflection(ctx, rNode, graph, budget, completionCh, trigger, msg, intent)
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
		go a.fireReflection(ctx, rNode, graph, budget, completionCh, trigger, intent)
		budget.ResetWaveCounters()
		batchCounter = 0
		log.Printf("[dag] batch reflection injected at %s", rID)
	}

	launchReady()

	for inflight > 0 {
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
							a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: rID, Node: graph.SnapshotNode(rID)})
							go a.fireReflection(ctx, rNode, graph, budget, completionCh, trigger, intent)
							budget.ResetWaveCounters()
							log.Printf("[dag] observer triggered reflection (%s)", obs.Reason)
						}
					}
				}
				launchReady()
				continue
			}

			if comp.Err != nil {
				graph.SetError(comp.NodeID, comp.Err)
				log.Printf("[dag] node %s (%s) failed: %v", comp.NodeID, node.ToolName, comp.Err)

				if node.Type == NodeMicroPlanner {
					graph.CascadeSkip(comp.NodeID)
					launchReady()
					continue
				}

				// Skip micro-planner for systemic failures — no repair will help.
				// Covers: our rate limit, IBE gate blocks, LLM API rate limits (HTTP 429)
				errMsg := comp.Err.Error()
				if strings.Contains(errMsg, "rate limit") ||
					strings.Contains(errMsg, "gate:") ||
					strings.Contains(errMsg, "HTTP 429") {
					log.Printf("[dag] systemic failure (%s), skipping micro-planner", node.ToolName)
					graph.CascadeSkip(comp.NodeID)
					launchReady()
					continue
				}

				if budget.LLMRemaining() > 1 && budget.TrySpawnNode("", true) {
					mpNode := &Node{
						Type:      NodeMicroPlanner,
						SpawnedBy: comp.NodeID,
						Tag:       "repair_" + node.Tag,
					}
					mpID := graph.AddNode(mpNode)
					graph.SetState(mpID, StateRunning)
					mpNode.StartedAt = time.Now()
					graph.AddChild(comp.NodeID, mpID)
					inflight++
					a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: mpID, Node: graph.SnapshotNode(mpID)})
					go a.fireMicroPlanner(ctx, mpNode, node, graph, budget, completionCh)
				} else {
					log.Printf("[dag] no LLM budget for micro-planner, cascading skip from %s", comp.NodeID)
					graph.CascadeSkip(comp.NodeID)
				}

			} else if node.Type == NodeMicroPlanner {
				graph.SetResult(comp.NodeID, comp.Result)
				failedNode := graph.Get(node.SpawnedBy)
				if failedNode != nil {
					newNodes, parseErr := parseMicroPlannerOutput(comp.Result, failedNode, graph, budget)
					if parseErr != nil {
						log.Printf("[dag] micro-planner parse failed: %v", parseErr)
						graph.CascadeSkip(failedNode.ID)
					} else {
						for _, nn := range newNodes {
							graph.AddChild(comp.NodeID, nn.ID)
						}
					}
				}

			} else if node.Type == NodeReflection || node.Type == NodeInterjection {
				reflectionInflight = false
				workSinceReflection = 0
				ref, parseErr := parseReflectionOutput(comp.Result)
				if parseErr != nil {
					log.Printf("[dag] reflection parse failed, continuing: %v", parseErr)
					graph.SetResult(comp.NodeID, comp.Result)
				} else {
					switch ref.Decision {
					case "continue":
						graph.SetResult(comp.NodeID, comp.Result)
						budget.ResetWaveCounters()
						log.Printf("[dag] reflection: continue (%s), wave counters reset", ref.Reason)

					case "conclude":
						graph.SetResult(comp.NodeID, ref.Verdict)
						graph.SkipAllPending()
						reflectionConcluded = true
						reflectionVerdict = ref.Verdict
						reflectionAggregate = ref.Aggregate
						log.Printf("[dag] reflection: conclude early (%s)", ref.Reason)

					case "replan":
						graph.SetResult(comp.NodeID, comp.Result)
						// Skip old pending nodes — the replan replaces them.
						// Without this, both old (param_ref injected) and new (replanned) fire.
						skipped := graph.SkipAllPending()
						budget.ResetWaveCounters()
						log.Printf("[dag] reflection: replan (%s), skipped %d pending, wave counters reset", ref.Reason, skipped)
						if len(ref.Nodes) > 0 {
							newNodes, graftErr := planStepsToNodes(ref.Nodes, graph, budget, a.registry, dagMode)
							if graftErr != nil {
								log.Printf("[dag] reflection replan graft failed: %v", graftErr)
							} else {
								for _, nn := range newNodes {
									if nn != nil {
										graph.AddChild(comp.NodeID, nn.ID)
									}
								}
							}
						}
					}
				}

			} else {
				// Tool node resolved successfully
				graph.SetResult(comp.NodeID, comp.Result)
				if node.Type == NodeSkill || node.Type == NodeActuator {
					workSinceReflection++
				}
				log.Printf("[dag] node %s (%s) resolved (%d bytes): %s",
					comp.NodeID, node.ToolName, len(comp.Result), Text.TruncateLog(comp.Result, 200))

				// ── Mode-specific post-completion logic ──
				switch dagMode {
				case DAGModeOrchestrator:
					// Spawn an orchestrator to evaluate this result
					if node.Type == NodeSkill && budget.TryObserverCall() {
						inflight++
						go a.fireObserver(ctx, node, graph, budget, completionCh, trigger, intent)
					}

				case DAGModeNReflect:
					// Track completions and inject reflection at batch threshold
					if node.Type == NodeSkill {
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

	// ── Ensure final reflection ──
	// If tool work completed since the last reflection, inject one now.
	// This loops: reflect → if replan → new nodes fire → loop back → reflect again.
	// Exits when reflection says "conclude", "continue", budget exhausted, or max replans hit.
	const maxReplans = 3
	replanCount := 0
	for workSinceReflection > 0 && !reflectionConcluded && budget.LLMRemaining() > 1 && ctx.Err() == nil {
		if !budget.TrySpawnNode("", true) {
			break
		}
		if replanCount >= maxReplans {
			log.Printf("[dag] max replans (%d) reached, forcing conclude", maxReplans)
			break
		}
		log.Printf("[dag] injecting final reflection (%d tool completions since last reflect, replan %d/%d)", workSinceReflection, replanCount, maxReplans)
		rNode := &Node{
			Type: NodeReflection,
			Tag:  "final_reflect",
		}
		rID := graph.AddNode(rNode)
		graph.SetState(rID, StateRunning)
		rNode.StartedAt = time.Now()
		inflight = 1
		a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: rID, Node: graph.SnapshotNode(rID)})
		go a.fireReflection(ctx, rNode, graph, budget, completionCh, trigger, intent)

		// Re-enter the main loop — handles reflection completion, replans, new nodes, everything
		for inflight > 0 {
			select {
			case <-ctx.Done():
				log.Printf("[dag] wall clock expired during final reflection")
				inflight = 0
			case comp := <-completionCh:
				inflight--
				node := graph.Get(comp.NodeID)
				if node == nil {
					continue
				}
				a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: node.ID, Node: graph.SnapshotNode(node.ID)})

				if node.Type == NodeReflection || node.Type == NodeInterjection {
					reflectionInflight = false
					workSinceReflection = 0
					ref, parseErr := parseReflectionOutput(comp.Result)
					if parseErr != nil {
						log.Printf("[dag] final reflection parse failed: %v", parseErr)
						graph.SetResult(comp.NodeID, comp.Result)
					} else {
						switch ref.Decision {
						case "conclude":
							graph.SetResult(comp.NodeID, ref.Verdict)
							reflectionConcluded = true
							reflectionVerdict = ref.Verdict
							reflectionAggregate = ref.Aggregate
							log.Printf("[dag] final reflection: conclude (%s)", ref.Reason)
						case "replan":
							replanCount++
							graph.SetResult(comp.NodeID, comp.Result)
							skipped := graph.SkipAllPending()
							budget.ResetWaveCounters()
							log.Printf("[dag] final reflection: replan #%d, skipped %d pending (%s)", replanCount, skipped, ref.Reason)
							grafted := 0
							if len(ref.Nodes) > 0 {
								newNodes, graftErr := planStepsToNodes(ref.Nodes, graph, budget, a.registry, dagMode)
								if graftErr != nil {
									log.Printf("[dag] final reflection replan graft failed: %v", graftErr)
								} else {
									for _, nn := range newNodes {
										if nn != nil {
											graph.AddChild(comp.NodeID, nn.ID)
											grafted++
										}
									}
								}
							}
							// If replan produced no new nodes (budget exhausted), force conclude
							// to prevent spinning on the same pending nodes.
							if grafted == 0 {
								log.Printf("[dag] replan produced 0 nodes (budget exhausted), forcing conclude")
								reflectionConcluded = true
							}
						default:
							graph.SetResult(comp.NodeID, comp.Result)
							log.Printf("[dag] final reflection: continue (%s)", ref.Reason)
						}
					}
				} else if comp.Err != nil {
					graph.SetError(comp.NodeID, comp.Err)
					log.Printf("[dag] final node %s failed: %v", comp.NodeID, comp.Err)
				} else {
					graph.SetResult(comp.NodeID, comp.Result)
					if node.Type == NodeSkill || node.Type == NodeActuator {
						workSinceReflection++
					}
					log.Printf("[dag] final node %s (%s) resolved (%d bytes): %s",
						comp.NodeID, node.ToolName, len(comp.Result), Text.TruncateLog(comp.Result, 200))
				}

				injectInterjection()
				launchReady()
			}
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

	dagCtx, cancel := context.WithTimeout(ctx, budget.WallClock)
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
	verdict, actions, aggErr = a.runAggregatorWithIntent(dagCtx, trigger, graph, resolvedIntent, trigger.History)
	if aggErr != nil {
		log.Printf("[dag] aggregator failed: %v", aggErr)
		return
	}

	log.Printf("[dag] verdict: %s", Text.TruncateLog(verdict, 200))

	// ── Phase 4: Execute actuator actions ──
	for _, action := range actions {
		if dagCtx.Err() != nil {
			break
		}
		result, execErr := a.executeToolNode(dagCtx, action.Tool, action.Params, trigger.AlertID, resolvedIntent, trigger.Scope)
		if execErr != nil {
			log.Printf("[dag] actuator %s failed: %v", action.Tool, execErr)
		} else {
			log.Printf("[dag] actuator %s: %s", action.Tool, Text.TruncateLog(result, 200))
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
		refCount, replanCount := graph.ReflectionStats()
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
			ReplanCount:     replanCount,
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
 *       Handles PlannerConversationalError by falling back to direct LLM chat
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

	dagCtx, cancel := context.WithTimeout(ctx, budget.WallClock)
	defer cancel()

	pr, err := a.runPlanAndSchedule(dagCtx, trigger, graph, budget)
	if err != nil {
		// If planner returned conversational text, surface it as the verdict
		// instead of failing the whole pipeline. This handles vague queries.
		var convErr *PlannerConversationalError
		if errors.As(err, &convErr) {
			if convErr.Text != "" {
				return &SyncResult{Verdict: convErr.Text}, nil
			}
			// Empty plan with no text — make a direct LLM call for conversational response
			log.Printf("[dag] empty plan, falling back to direct response")
			query := ""
			if trigger.Data != nil {
				var d map[string]string
				if json.Unmarshal(trigger.Data, &d) == nil {
					query = d["query"]
				}
			}
			if query != "" {
				resp, llmErr := a.llm.Complete(dagCtx, &llm.ChatRequest{
					Messages:    BuildMessagesWithHistory(a.soulPrompt, query, trigger.History),
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

	// Auto mode: let the reflector decide
	if aggMode == -1 && pr.ReflectionVerdict != "" {
		if pr.ReflectionAggregate != nil && *pr.ReflectionAggregate {
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
		if !budget.TrySpawnNode("", true) {
			return nil, fmt.Errorf("budget exhausted before aggregator")
		}

		// Select LLM client based on agg_mode
		aggClient := a.executor // default: executor model (agg_mode=1)
		aggLabel := "executor"
		if aggMode == 2 {
			aggClient = a.llm // reasoning model
			aggLabel = "reasoning"
		}
		log.Printf("[dag] aggregator using %s model (agg_mode=%d)", aggLabel, aggMode)

		a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: "aggregator", Node: &NodeInfo{ID: "aggregator", Type: "aggregator", State: "running", Tag: "synthesize"}})

		var aggErr error
		verdict, actions, aggErr = a.runAggregatorWithClient(dagCtx, trigger, graph, resolvedIntent, trigger.History, aggClient)
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
		refCount, replanCount := graph.ReflectionStats()
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
			ReplanCount:     replanCount,
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

// Dispatcher functions (fireNode, resolveInjections, extractJSONField,
// executeToolNode, toolThrottle) are in dispatcher.go.
