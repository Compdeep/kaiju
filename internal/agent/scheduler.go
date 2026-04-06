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

// ── Scheduler: DAG execution engine ─────────────────────────────────────────
//
// Node roles:
//   Planner       — LLM call that decomposes user query into a DAG of steps
//   Tool          — executes a registered tool (bash, file_read, web_search, etc)
//   Compute       — LLM code generation: deep=architect, shallow=coder
//   Reflection    — checkpoint that evaluates evidence and decides continue/replan/conclude
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
//   - After a reflector replan, stored validators are re-grafted to verify the fix
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
	//   - mode: chat / meta / investigate (chat and meta short-circuit the planner)
	//   - intent: a rank from the intent registry (used when trigger intent is Auto)
	//   - required_categories: tool categories the plan must include
	if a.cfg.ClassifierEnabled {
		if !budget.TrySpawnNode("", true) {
			return nil, fmt.Errorf("budget exhausted before preflight")
		}
		a.preflight = a.preflightQuery(ctx, formatTrigger(trigger), trigger.History)
		a.activeCards = a.preflight.Skills
		log.Printf("[dag] preflight: mode=%s intent=%s skills=%v categories=%v",
			a.preflight.Mode, a.preflight.Intent, a.preflight.Skills, a.preflight.RequiredCategories)

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

		// Short-circuit chat/meta queries — skip the planner entirely.
		// Only fires in interactive mode (autonomous overrides above).
		if a.preflight.Mode == "chat" || a.preflight.Mode == "meta" {
			log.Printf("[dag] preflight short-circuit: mode=%s, skipping planner", a.preflight.Mode)
			return nil, &PlannerConversationalError{Text: ""}
		}
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
		// Cap by user's scope ceiling (planner can't escalate beyond what
		// the user is allowed to request)
		if trigger.Scope != nil && gates.Intent(trigger.Scope.MaxIntent) < intent {
			log.Printf("[dag] planner inferred %s but user scope caps at %d, capping", intent, trigger.Scope.MaxIntent)
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
				reflectionInflight = true
				go a.fireReflection(ctx, n, graph, budget, completionCh, trigger, intent)
			} else {
				// NodeCompute and NodeTool both flow through fireNode. Compute
				// is a ContextualExecutor and the dispatcher routes it to
				// ExecuteWithContext. The NodeCompute classification remains
				// for post-completion graft logic below.
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

			// ── Retry proxy: a replacement node succeeded — copy result
			// back to the original node so its dependents unblock. ──
			if comp.Err == nil && node.ProxyForID != "" {
				graph.SetResult(comp.NodeID, comp.Result)
				original := graph.Get(node.ProxyForID)
				if original != nil {
					graph.SetResult(original.ID, comp.Result)
					graph.SetState(original.ID, StateResolved)
					log.Printf("[dag] retry proxy: %s succeeded, unblocking %s", comp.NodeID, original.ID)
				}
				launchReady()
				continue
			}

			if comp.Err != nil {
				log.Printf("[dag] node %s (%s) failed: %v", comp.NodeID, node.ToolName, comp.Err)
				appendWorklog(a.cfg.Workspace, node.Tag, "FAILED", Text.TruncateLog(comp.Err.Error(), 150))

				// If this is a retry proxy that failed, bump the original's
				// retry count. If retries exhausted, resolve the original
				// with the failure so dependents unblock.
				if node.ProxyForID != "" {
					original := graph.Get(node.ProxyForID)
					if original != nil {
						original.RetryCount++
						if original.RetryCount >= a.cfg.MaxNodeRetries {
							graph.SetError(original.ID, comp.Err)
							log.Printf("[dag] retry proxy: %s exhausted %d retries, unblocking %s with failure", comp.NodeID, a.cfg.MaxNodeRetries, original.ID)
							launchReady()
							continue
						}
					}
				}

				if node.Type == NodeMicroPlanner {
					// Micro-planner itself failed — resolve the original
					// with the failure so dependents unblock.
					failedNode := graph.Get(node.SpawnedBy)
					if failedNode != nil && failedNode.State == StateRunning && failedNode.ProxyForID == "" {
						graph.SetError(failedNode.ID, comp.Err)
					}
					graph.SetError(comp.NodeID, comp.Err)
					launchReady()
					continue
				}

				graph.SetError(comp.NodeID, comp.Err)

				// Skip micro-planner for systemic failures — no repair will help.
				errMsg := comp.Err.Error()
				if strings.Contains(errMsg, "rate limit") ||
					strings.Contains(errMsg, "gate:") ||
					strings.Contains(errMsg, "HTTP 429") {
					log.Printf("[dag] systemic failure (%s), skipping micro-planner", node.ToolName)
					graph.CascadeSkip(comp.NodeID)
					launchReady()
					continue
				}

				// Circuit breaker: skip microplanner after 5 accumulated failures
				if hasCircuitBroken(node) {
					log.Printf("[dag] circuit breaker: %s has 5+ accumulated failures, skipping microplanner", node.Tag)
					graph.CascadeSkip(comp.NodeID)
					launchReady()
					continue
				}

				// Retry proxy pattern: if other nodes depend on this one,
				// keep it "running" so dependents stay blocked while the
				// micro-planner tries a replacement. Nodes with no dependents
				// just fail normally — no point holding them open.
				maxRetries := a.cfg.MaxNodeRetries
				if maxRetries <= 0 {
					maxRetries = 2
				}
				hasDeps := graph.HasPendingDependents(comp.NodeID)
				if hasDeps && node.RetryCount < maxRetries && budget.LLMRemaining() > 1 {
					// Keep node in running state — don't resolve, don't cascade.
					// Revert the SetError above so the node stays "running".
					graph.SetState(comp.NodeID, StateRunning)
					node.RetryCount++
					if a.spawnMicroPlannerNode(ctx, node, comp, graph, budget, completionCh, trigger) {
						inflight++
					} else {
						// Micro-planner spawn failed — resolve with failure
						graph.SetError(comp.NodeID, comp.Err)
						graph.CascadeSkip(comp.NodeID)
					}
				} else if budget.LLMRemaining() > 1 {
					// No dependents or retries exhausted — normal micro-planner
					// (node is already marked failed, dependents can proceed)
					if a.spawnMicroPlannerNode(ctx, node, comp, graph, budget, completionCh, trigger) {
						inflight++
					}
				} else {
					log.Printf("[dag] no budget for micro-planner, cascading skip from %s", comp.NodeID)
					graph.CascadeSkip(comp.NodeID)
				}

			} else if node.Type == NodeMicroPlanner {
				graph.SetResult(comp.NodeID, comp.Result)
				failedNode := graph.Get(node.SpawnedBy)
				if failedNode != nil {
					newNodes, parseErr := parseMicroPlannerOutput(comp.Result, failedNode, graph, budget)
					if parseErr != nil {
						log.Printf("[dag] micro-planner parse failed: %v", parseErr)
						// If the original was held open as a proxy, resolve it now
						if failedNode.State == StateRunning {
							graph.SetError(failedNode.ID, fmt.Errorf("micro-planner parse failed: %v", parseErr))
						}
						graph.CascadeSkip(failedNode.ID)
					} else {
						for _, nn := range newNodes {
							// Tag replacement nodes as proxies for the original
							nn.ProxyForID = failedNode.ID
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
						skipped := graph.SkipAllPending()
						budget.ResetWaveCounters()
						log.Printf("[dag] reflection: replan (%s), skipped %d pending, wave counters reset", ref.Reason, skipped)
						var replanGrafted int
						if len(ref.Nodes) > 0 {
							newNodes, graftErr := planStepsToNodes(ref.Nodes, graph, budget, a.registry, dagMode)
							if graftErr != nil {
								log.Printf("[dag] reflection replan graft failed: %v", graftErr)
							} else {
								replanIDs := make([]string, 0, len(newNodes))
								for _, nn := range newNodes {
									if nn != nil {
										graph.AddChild(comp.NodeID, nn.ID)
										replanIDs = append(replanIDs, nn.ID)
										replanGrafted++
									}
								}
								// Re-graft stored validators after replan
								if replanGrafted > 0 && len(graph.Validators) > 0 {
									rv := 0
									for _, v := range graph.Validators {
										if !budget.TrySpawnNode("bash", false) {
											break
										}
										vNode := &Node{
											Type:      NodeTool,
											ToolName:  "bash",
											Params:    map[string]any{"command": v.Check, "timeout_sec": 15},
											DependsOn: append([]string{}, replanIDs...),
											SpawnedBy: comp.NodeID,
											Tag:       "revalidate_" + sanitizeTag(v.Name),
											Source:    "builtin",
										}
										vID := graph.AddNode(vNode)
										graph.AddChild(comp.NodeID, vID)
										a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: vID, Node: graph.SnapshotNode(vID)})
										rv++
									}
									if rv > 0 {
										log.Printf("[dag] re-grafted %d validators after replan", rv)
									}
								}
							}
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
					appendWorklog(a.cfg.Workspace, node.Tag, "BASH_ERROR", Text.TruncateLog(comp.Result, 150))

					if hasCircuitBroken(node) {
						log.Printf("[dag] circuit breaker: %s has 5+ accumulated failures, skipping", node.Tag)
						graph.CascadeSkip(comp.NodeID)
					} else if budget.LLMRemaining() > 1 {
						if a.spawnMicroPlannerNode(ctx, node, comp, graph, budget, completionCh, trigger) {
							inflight++
						}
					}
					injectInterjection()
					launchReady()
					continue
				}

				if node.Type == NodeTool || node.Type == NodeCompute || node.Type == NodeActuator {
					workSinceReflection++
					appendWorklog(a.cfg.Workspace, node.Tag, "OK", fmt.Sprintf("%s: %s", node.ToolName, Text.TruncateLog(comp.Result, 100)))
				}
				log.Printf("[dag] node %s (%s) resolved (%d bytes): %s",
					comp.NodeID, node.ToolName, len(comp.Result), Text.TruncateLog(comp.Result, 500))

				// ── Compute plan follow-up graft ──
				if node.Type == NodeCompute {
					var cr struct {
						Type       string          `json:"type"`
						Setup      []string        `json:"setup,omitempty"`
						FollowUp   json.RawMessage `json:"follow_up,omitempty"`
						Execute    string          `json:"execute,omitempty"`
						Validation []struct {
							Name   string `json:"name"`
							Check  string `json:"check"`
							Expect string `json:"expect"`
						} `json:"validation,omitempty"`
					}
					if json.Unmarshal([]byte(comp.Result), &cr) == nil && cr.Type == "blueprint" && len(cr.FollowUp) > 0 {
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
							if !budget.TrySpawnNode("compute", true) {
								log.Printf("[dag] budget exhausted, grafted %d of %d tasks", i, len(followUps))
								break
							}
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
							if computeIDs[i] == "" {
								continue
							}
							// One-shot execute command (e.g. "npm run build")
							if execCmd, ok := fu.Params["execute"].(string); ok && execCmd != "" {
								if budget.TrySpawnNode("bash", false) {
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
							// Long-running service (e.g. "node server.js")
							if svc, ok := fu.Params["service"].(map[string]any); ok {
								svcCmd, _ := svc["command"].(string)
								svcName, _ := svc["name"].(string)
								if svcCmd != "" {
									if svcName == "" {
										svcName = computeNodes[i].Tag + "_svc"
									}
									if budget.TrySpawnNode("service", false) {
										svcNode := &Node{
											Type:      NodeTool,
											ToolName:  "service",
											Params:    map[string]any{"action": "start", "command": svcCmd, "name": svcName},
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

						// Phase 4: validation wave — architect-declared checks.
						// Each validation entry becomes a bash node running its
						// check command, depending on all Phase 1-3 grafted
						// nodes so it runs only after setup + coders complete.
						// Reflector sees pass/fail as structured evidence of
						// goal achievement.
						if len(cr.Validation) > 0 {
							// Store validators on the graph for replay after replans.
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
									Params:    map[string]any{"command": v.Check, "timeout_sec": 15},
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

					// ── Compute execute/service graft ──
					if node.Type == NodeCompute {
						var execResult struct {
							Type    string `json:"type"`
							Execute string `json:"execute,omitempty"`
							Service *struct {
								Command string `json:"command"`
								Name    string `json:"name"`
							} `json:"service,omitempty"`
						}
						if json.Unmarshal([]byte(comp.Result), &execResult) == nil && execResult.Type == "result" {
							// One-shot execute command
							if execResult.Execute != "" {
								if budget.TrySpawnNode("bash", false) {
									execNode := &Node{
										Type:      NodeTool,
										ToolName:  "bash",
										Params:    map[string]any{"command": execResult.Execute},
										DependsOn: []string{comp.NodeID},
										SpawnedBy: comp.NodeID,
										Tag:       node.Tag + "_exec",
										Source:    "builtin",
									}
									eID := graph.AddNode(execNode)
									graph.AddChild(comp.NodeID, eID)
									rewriteDependentsExcluding(graph, comp.NodeID, eID, eID)
									log.Printf("[dag] compute → grafted execute bash node %s: %s", eID, execResult.Execute)
									a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: eID, Node: graph.SnapshotNode(eID)})
								}
							}
							// Long-running service — start with nohup, log to file, capture PID
							if execResult.Service != nil && execResult.Service.Command != "" {
								svcName := execResult.Service.Name
								if svcName == "" {
									svcName = node.Tag
								}
								logFile := fmt.Sprintf("%s.log", svcName)
								svcCmd := fmt.Sprintf("nohup %s > %s 2>&1 & echo $!", execResult.Service.Command, logFile)
								if budget.TrySpawnNode("bash", false) {
									svcNode := &Node{
										Type:      NodeTool,
										ToolName:  "bash",
										Params:    map[string]any{"command": svcCmd},
										DependsOn: []string{comp.NodeID},
										SpawnedBy: comp.NodeID,
										Tag:       node.Tag + "_svc",
										Source:    "builtin",
									}
									sID := graph.AddNode(svcNode)
									graph.AddChild(comp.NodeID, sID)
									rewriteDependentsExcluding(graph, comp.NodeID, sID, sID)
									log.Printf("[dag] compute → grafted service bash node %s: %s (log: %s)", sID, execResult.Service.Command, logFile)
									a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: sID, Node: graph.SnapshotNode(sID)})
									// Log to worklog so Kaiju remembers the service
									appendWorklog(a.cfg.Workspace, svcName, "SERVICE", fmt.Sprintf("started: %s (log: %s)", execResult.Service.Command, logFile))
								}
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

	// ── Ensure final reflection ──
	// If tool work completed since the last reflection, inject one now.
	// This loops: reflect → if replan → new nodes fire → loop back → reflect again.
	// Exits when reflection says "conclude", "continue", budget exhausted, or max replans hit.
	maxReplans := a.cfg.MaxReplans
	if maxReplans <= 0 {
		maxReplans = 3
	}
	replanCount := 0
	for workSinceReflection > 0 && !reflectionConcluded && budget.LLMRemaining() > 2 && ctx.Err() == nil {
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
							var replanNodeIDs []string
							if len(ref.Nodes) > 0 {
								newNodes, graftErr := planStepsToNodes(ref.Nodes, graph, budget, a.registry, dagMode)
								if graftErr != nil {
									log.Printf("[dag] final reflection replan graft failed: %v", graftErr)
								} else {
									for _, nn := range newNodes {
										if nn != nil {
											graph.AddChild(comp.NodeID, nn.ID)
											replanNodeIDs = append(replanNodeIDs, nn.ID)
											grafted++
										}
									}
								}
							}
							// Re-graft stored validators after replan nodes so the
							// original architect checks run again to verify the fix.
							if grafted > 0 && len(graph.Validators) > 0 {
								revalidated := 0
								for _, v := range graph.Validators {
									if !budget.TrySpawnNode("bash", false) {
										break
									}
									vTag := "revalidate_" + sanitizeTag(v.Name)
									vNode := &Node{
										Type:      NodeTool,
										ToolName:  "bash",
										Params:    map[string]any{"command": v.Check, "timeout_sec": 15},
										DependsOn: append([]string{}, replanNodeIDs...),
										SpawnedBy: comp.NodeID,
										Tag:       vTag,
										Source:    "builtin",
									}
									vID := graph.AddNode(vNode)
									graph.AddChild(comp.NodeID, vID)
									a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: vID, Node: graph.SnapshotNode(vID)})
									revalidated++
								}
								if revalidated > 0 {
									log.Printf("[dag] re-grafted %d validators after replan", revalidated)
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
					if node.Type != NodeMicroPlanner && budget.LLMRemaining() > 2 {
						if a.spawnMicroPlannerNode(ctx, node, comp, graph, budget, completionCh, trigger) {
							inflight++
						}
					}
				} else {
					graph.SetResult(comp.NodeID, comp.Result)

					// Detect bash errors in final loop
					if bashErr, isBash := isBashError(comp.Result); isBash && node.Type == NodeTool && node.ToolName == "bash" {
						log.Printf("[dag] final node %s (bash) completed with error: %s", comp.NodeID, Text.TruncateLog(comp.Result, 500))
						graph.SetError(comp.NodeID, bashErr)
						node.Error = bashErr
						if !hasCircuitBroken(node) && budget.LLMRemaining() > 2 {
							if a.spawnMicroPlannerNode(ctx, node, comp, graph, budget, completionCh, trigger) {
								inflight++
							}
						}
						continue
					}

					if node.Type == NodeTool || node.Type == NodeCompute || node.Type == NodeActuator {
						workSinceReflection++
					}
					log.Printf("[dag] final node %s (%s) resolved (%d bytes): %s",
						comp.NodeID, node.ToolName, len(comp.Result), Text.TruncateLog(comp.Result, 500))
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
				if a.preflight != nil && len(a.preflight.Skills) > 0 {
					var skillSections strings.Builder
					skillSections.WriteString("\n\n## Active Skill Guidance\n\n")
					for _, key := range a.preflight.Skills {
						if card, ok := a.capabilities[key]; ok {
							skillSections.WriteString("### " + card.Key + "\n" + card.Body + "\n\n")
						}
						if sk, ok := a.skillGuidance[key]; ok {
							skillSections.WriteString("### " + sk.Name() + "\n" + sk.Description() + "\n\n")
						}
					}
					chatPrompt += skillSections.String()
				}
				chatPrompt += "\n\nIMPORTANT: You are in chat-only mode — you cannot execute tools, run commands, or build anything right now. Never say \"I'll do X\" or \"Let me build X\" because you can't follow through. If the user is asking you to do something, answer their question or give information, but do not promise actions you cannot take."
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
		// Aggregator is exempt from budget — it must always run to give the user a response
		budget.TrySpawnNode("", true) // charge if possible, but don't block

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

/*
 * spawnMicroPlannerNode creates and fires a microplanner for a failed node.
 * desc: Shared helper to avoid duplicating the microplanner spawn block.
 *       Returns true if spawned, false if budget exhausted.
 */
func (a *Agent) spawnMicroPlannerNode(ctx context.Context, failedNode *Node, comp nodeCompletion,
	graph *Graph, budget *Budget, completionCh chan nodeCompletion, trigger Trigger) bool {
	if !budget.TrySpawnNode("", true) {
		log.Printf("[dag] no LLM budget for micro-planner, cascading skip from %s", comp.NodeID)
		graph.CascadeSkip(comp.NodeID)
		return false
	}
	mpNode := &Node{
		Type:      NodeMicroPlanner,
		SpawnedBy: comp.NodeID,
		Tag:       "repair_" + failedNode.Tag,
	}
	mpID := graph.AddNode(mpNode)
	graph.SetState(mpID, StateRunning)
	mpNode.StartedAt = time.Now()
	graph.AddChild(comp.NodeID, mpID)
	a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: mpID, Node: graph.SnapshotNode(mpID)})
	go a.fireMicroPlanner(ctx, mpNode, failedNode, graph, budget, completionCh, trigger)
	return true
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
 * hasCircuitBroken checks if a node has exceeded the retry limit.
 * desc: Returns true if the node has 5+ accumulated hints.
 */
func hasCircuitBroken(node *Node) bool {
	if hints, ok := node.Params["hints"].([]any); ok && len(hints) >= 5 {
		return true
	}
	return false
}

// Dispatcher functions (fireNode, resolveInjections, extractJSONField,
// executeToolNode, toolThrottle) are in dispatcher.go.
