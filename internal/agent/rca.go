package agent

/*
 * Holmes — ReAct root-cause investigator.
 *
 * desc: Holmes is the diagnostic phase of the failure-handling pipeline. It
 *       sits between the reflector (which classifies symptoms) and the
 *       microplanner (which prescribes fixes). The reflector says "something
 *       is wrong here is what I see in the logs". The microplanner says "here
 *       is the patch to apply". Holmes fills the gap in between: he gathers
 *       evidence with read-only tool calls, forms hypotheses, refines them
 *       against fresh observations, and emits a structured root-cause analysis
 *       that the microplanner consumes as authoritative.
 *
 *       Holmes runs as a ReAct loop. Each iteration is a real graph node
 *       (NodeHolmes) so the investigation is fully visible in the DAG trace.
 *       The scheduler grafts iterations one at a time: a Holmes LLM call
 *       picks a tool, the tool runs as the next node depending on it, then
 *       Holmes fires again on the result. The loop terminates when Holmes
 *       declares conclude=true, exhausts MaxHolmesIters, or runs out of
 *       budget.
 *
 *       Holmes writes in Holmes voice. This is not theatrics — first-person
 *       deductive prose forces the LLM to articulate evidence-based reasoning
 *       instead of hedging with "possibly" / "likely" / "might be". The prose
 *       is fed back to Holmes on the next iteration so each round of
 *       deduction builds on the last.
 */

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/gates"
	"github.com/Compdeep/kaiju/internal/agent/llm"
)

/*
 * RCAReport is Holmes's final structured output once the investigation
 * concludes. It is consumed verbatim by the microplanner.
 */
type RCAReport struct {
	RootCause         string   `json:"root_cause"`
	Evidence          []string `json:"evidence"`
	Confidence        string   `json:"confidence"` // "high" | "medium" | "low"
	SuggestedStrategy string   `json:"suggested_strategy"`
}

/*
 * holmesOutput is the per-iteration response from the Holmes LLM. It
 * carries one or more actions to take next OR a final RCA conclusion
 * (when Conclude is true). Multiple actions run in parallel; Holmes
 * sees all results on the next iteration.
 */
type holmesOutput struct {
	Reasoning  string          `json:"reasoning"`           // Holmes-voice prose
	Hypothesis string          `json:"hypothesis"`          // current working theory
	Actions    []holmesAction  `json:"actions,omitempty"`   // parallel read-only diagnostics
	Action     *holmesAction   `json:"action,omitempty"`    // legacy single-action form (normalized into Actions during parse)
	Conclude   bool            `json:"conclude"`            // true when investigation is done
	RCA        *RCAReport      `json:"rca,omitempty"`       // populated when Conclude is true
}

type holmesAction struct {
	Tool   string         `json:"tool"`
	Params map[string]any `json:"params"`
}

/*
 * HolmesState is the per-investigation accumulator. It lives in the params
 * of each Holmes node so the next iteration can pick up where the last left
 * off. Each iteration appends a HolmesTurn to History; the LLM sees the
 * full history on every call so its deductions build on prior evidence.
 */
type HolmesState struct {
	Problem             string         `json:"problem"`                        // original symptom from reflector
	InvestigationCount  int            `json:"investigation_count"`            // which investigation cycle this Holmes run belongs to
	Iter                int            `json:"iter"`                           // 1-indexed iteration counter
	MaxIter             int            `json:"max_iter"`                       // termination cap
	History             []HolmesTurn   `json:"history"`                        // every prior thought + action + observation
	Hypotheses          []string       `json:"hypotheses"`                     // every theory tested so far (cycle detection)
	LastActionNodeIDs   []string       `json:"last_action_node_ids,omitempty"` // node IDs of the most recent parallel actions
	LastActionNodeID    string         `json:"last_action_node_id,omitempty"`  // DEPRECATED: single-action compat; read during migration, never written
}

/*
 * HolmesTurn is one step in the investigation log. Reasoning + hypothesis
 * are emitted by the LLM. Actions are what Holmes chose to run in parallel.
 * Observations are the results (filled in by the scheduler before the next
 * iteration fires), one per action in the same order.
 */
type HolmesTurn struct {
	Iter         int             `json:"iter"`
	Reasoning    string          `json:"reasoning"`
	Hypothesis   string          `json:"hypothesis"`
	Actions      []holmesAction  `json:"actions,omitempty"`
	Observations []string        `json:"observations,omitempty"`
	// Legacy fields for backward compat with in-flight investigations
	Action      *holmesAction `json:"action,omitempty"`
	Observation string          `json:"observation,omitempty"`
}

/*
 * spawnFirstHolmes creates the very first Holmes node for an investigation
 * and seeds its state with the problem statement from the reflector.
 * desc: Called by the scheduler when the reflector classifies "investigate".
 *       The returned node is added to the graph but NOT yet fired — the
 *       caller does that. This keeps state construction in one place
 *       (rca.go) and scheduler logic separate.
 * param: graph - the investigation graph.
 * param: problem - the symptom blob from the reflector.
 * param: parentID - node ID of the reflection that triggered Holmes.
 * param: investigationCount - which investigation cycle this is (for tagging).
 * param: maxIter - iteration cap from agent config.
 * return: the new Holmes node, or nil + error if state setup failed.
 */
func spawnFirstHolmes(graph *Graph, problem, parentID string, investigationCount, maxIter int) (*Node, error) {
	state := &HolmesState{
		Problem:            problem,
		InvestigationCount: investigationCount,
		Iter:               1,
		MaxIter:            maxIter,
		History:            nil,
	}
	sNode := &Node{
		Type:      NodeHolmes,
		Tag:       fmt.Sprintf("analyse_%d_iter_1", investigationCount),
		SpawnedBy: parentID,
	}
	if err := saveHolmesState(sNode, state); err != nil {
		return nil, err
	}
	id := graph.AddNode(sNode)
	graph.SetState(id, StateRunning)
	graph.AddChild(parentID, id)
	return sNode, nil
}

/*
 * spawnNextHolmes creates the next Holmes iteration after an action's
 * observation has been recorded.
 * desc: Builds the new node with state advanced by one iteration. The
 *       actionNodeID is stored on the new state so launchReady can find the
 *       action result by ID instead of guessing from DependsOn. Caller is
 *       responsible for setting depends_on so the new iteration runs after
 *       the action node completes.
 * param: graph - the investigation graph.
 * param: prevState - the state from the previous iteration (will be cloned + advanced).
 * param: prevTurn - the most recent HolmesTurn to append (with observation filled in).
 * param: parentID - the node spawning this iteration (for AddChild).
 * param: investigationCount - which investigation cycle this is.
 * param: actionNodeID - graph node ID of the action whose result this iteration must read.
 * return: the new Holmes node + error.
 */
func spawnNextHolmes(graph *Graph, prevState *HolmesState, prevTurn HolmesTurn, parentID string, investigationCount int, actionNodeIDs []string) (*Node, error) {
	nextState := &HolmesState{
		Problem:            prevState.Problem,
		InvestigationCount: investigationCount,
		Iter:               prevState.Iter + 1,
		MaxIter:            prevState.MaxIter,
		History:            append([]HolmesTurn{}, prevState.History...),
		Hypotheses:         append([]string{}, prevState.Hypotheses...),
		LastActionNodeIDs:  actionNodeIDs,
	}
	nextState.History = append(nextState.History, prevTurn)
	if prevTurn.Hypothesis != "" {
		nextState.Hypotheses = append(nextState.Hypotheses, prevTurn.Hypothesis)
	}
	sNode := &Node{
		Type:      NodeHolmes,
		Tag:       fmt.Sprintf("analyse_%d_iter_%d", investigationCount, nextState.Iter),
		SpawnedBy: parentID,
	}
	if err := saveHolmesState(sNode, nextState); err != nil {
		return nil, err
	}
	id := graph.AddNode(sNode)
	graph.SetState(id, StatePending)
	graph.AddChild(parentID, id)
	return sNode, nil
}

/*
 * dispatchMicroplannerWithRCA fires the microplanner after Holmes has
 * concluded. The Holmes RCA is serialised into the microplanner's params so
 * the assembleDebuggerPrompt function can render it as the authoritative
 * diagnosis section.
 * desc: This is the bridge from the investigation phase to the prescription
 *       phase. The scheduler calls this when a Holmes node completes with
 *       conclude=true. The microplanner inherits the addressing list (failed
 *       node IDs that should be marked superseded once the fix succeeds) from
 *       the addressingByInvestigation map snapshotted at Holmes dispatch time.
 * param: ctx - context for the LLM call.
 * param: a - the agent.
 * param: graph - the investigation graph.
 * param: budget - execution budget.
 * param: completionCh - completion channel.
 * param: trigger - investigation trigger.
 * param: parentID - the Holmes node ID this microplanner was spawned from.
 * param: investigationCount - which investigation cycle this is (used for tagging).
 * param: problem - original problem from reflector (preserved for context).
 * param: rca - Holmes's final RCA.
 * param: addressing - failed node IDs to mark superseded once fixed.
 * param: intent - the resolved investigation intent (post-auto-inference).
 * return: the microplanner node ID and any error.
 */
func dispatchMicroplannerWithRCA(ctx context.Context, a *Agent, graph *Graph, budget *Budget,
	completionCh chan<- nodeCompletion, trigger Trigger, parentID string, investigationCount int,
	problem string, rca *RCAReport, addressing []string, intent gates.Intent) (string, error) {

	if !budget.TrySpawnNode("", true) {
		return "", fmt.Errorf("no budget for microplanner")
	}

	// Marshal the RCA into the params so assembleDebuggerPrompt can read it.
	rcaJSON, err := json.Marshal(rca)
	if err != nil {
		return "", fmt.Errorf("marshal rca: %w", err)
	}

	mpNode := &Node{
		Type: NodeMicroPlanner,
		Tag:  fmt.Sprintf("debug_%d", investigationCount),
		Params: map[string]any{
			"problem": problem,
			"rca":     string(rcaJSON),
		},
		SpawnedBy:         parentID,
		AddressesFailures: addressing,
	}
	mpID := graph.AddNode(mpNode)
	graph.SetState(mpID, StateRunning)
	graph.AddChild(parentID, mpID)
	a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: mpID, Node: graph.SnapshotNode(mpID)})

	// Build microplanner context. Same shape as before — curator over worklog
	// + failures using the problem as the query, plus blueprint sections,
	// workspace tree, and debug skill guidance. The RCA is conveyed via params
	// (above) not the gate.
	mpCtxReq := ContextRequest{
		Query: problem,
		QuerySources: Sources(
			Worklog(50, "all"),
			NodeReturns("failures"),
		),
		ReturnSources: Sources(
			BlueprintSections(
				"Goal",
				"Architecture",
				"Directory Structure",
				"Build System",
				"Services",
				"Files",
			),
			WorkspaceTree(4),
			SkillGuidance([]string{"Debug"}),
		),
		MaxBudget: 16000,
	}
	mpCtx, ctxErr := graph.Context.Get(ctx, mpCtxReq)
	if ctxErr != nil {
		log.Printf("[dag] microplanner context build failed: %v", ctxErr)
		mpCtx = &ContextResponse{Sources: map[string]string{}}
	}

	go a.fireMicroPlanner(ctx, mpNode, graph, budget, completionCh, mpCtx, trigger, intent)
	log.Printf("[dag] dispatched microplanner %s after holmes RCA: %s", mpID, Text.TruncateLog(rca.RootCause, 200))
	return mpID, nil
}

/*
 * holmesSeenHypothesis returns true if the given hypothesis (case-insensitive
 * substring) is already in the state's history. Used for cycle detection.
 */
func holmesSeenHypothesis(state *HolmesState, h string) bool {
	if h == "" {
		return false
	}
	needle := strings.ToLower(strings.TrimSpace(h))
	for _, prior := range state.Hypotheses {
		if strings.ToLower(strings.TrimSpace(prior)) == needle {
			return true
		}
	}
	return false
}

/*
 * fireHolmes runs ONE iteration of the Holmes investigation loop.
 * desc: Builds the user prompt from the accumulated HolmesState (problem +
 *       prior turns), calls the reasoning LLM with the holmes tool def, and
 *       returns the parsed output via the completion channel as raw JSON.
 *       The scheduler then either grafts the chosen action as the next node
 *       OR (if conclude=true) dispatches the microplanner with the RCA.
 *
 *       Holmes has access to ALL tools the user's intent allows — the
 *       prompt instructs read-only behaviour, but we do not gate at the tool
 *       level (per design: Holmes might need curl, ps, or even cd into the
 *       project to investigate properly).
 * param: ctx - context for the LLM call.
 * param: sNode - the Holmes node in the graph (params carry the state).
 * param: graph - the investigation graph.
 * param: budget - the execution budget.
 * param: ch - channel to send the completion result.
 * param: trigger - the investigation trigger.
 * param: intent - the resolved investigation intent (used for tool filtering;
 *                 must be the post-auto-inference value, not trigger.Intent()
 *                 which returns IntentAuto for chat queries and would filter
 *                 every tool out of the prompt).
 */
func (a *Agent) fireHolmes(ctx context.Context, sNode *Node, graph *Graph,
	budget *Budget, ch chan<- nodeCompletion, trigger Trigger, intent gates.Intent) {

	state, err := loadHolmesState(sNode)
	if err != nil {
		ch <- nodeCompletion{NodeID: sNode.ID, Err: fmt.Errorf("holmes state: %w", err)}
		return
	}

	// Holmes gets THREE things from the gate:
	//   1. Worklog tail — the "crime scene": last few timestamped events.
	//   2. Skill guidance — the "detective's notebook": domain-specific
	//      investigation procedures.
	//   3. Workspace tree — the "floor plan": project directory structure
	//      so Holmes knows where to look instead of guessing paths.
	// Gate failure is fail-open: Holmes proceeds without context.
	var gateCtx *ContextResponse
	if graph != nil && graph.Context != nil {
		var gerr error
		gateCtx, gerr = graph.Context.Get(ctx, ContextRequest{
			ReturnSources: Sources(
				Worklog(10, "all"),
				SkillGuidance([]string{"Debug"}),
				WorkspaceTree(3),
			),
			MaxBudget: 12000,
		})
		if gerr != nil {
			log.Printf("[dag] holmes gate fetch failed (continuing without crime scene): %v", gerr)
			gateCtx = &ContextResponse{Sources: map[string]string{}}
		}
	}

	sysPrompt := holmesPrompt + a.fleetSection()
	userPrompt := assembleHolmesPrompt(state, trigger, a, intent, gateCtx)

	log.Printf("[dag] holmes iter %d/%d for %s (%d bytes)", state.Iter, state.MaxIter, sNode.Tag, len(userPrompt))

	messages := []llm.Message{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: userPrompt},
	}

	started := time.Now()
	resp, llmErr := a.llm.Complete(ctx, &llm.ChatRequest{
		Messages:    messages,
		Tools:       []llm.ToolDef{holmesToolDef()},
		ToolChoice:  "required",
		Temperature: a.cfg.Temperature,
		MaxTokens:   1024,
	})

	trace := LLMTrace{
		AlertID:  trigger.AlertID,
		NodeID:   sNode.ID,
		NodeType: "holmes",
		Tag:      sNode.Tag,
		Model:    "reasoning",
		Started:  started,
		Input: map[string]string{
			"iter":    fmt.Sprintf("%d/%d", state.Iter, state.MaxIter),
			"problem": Text.TruncateLog(state.Problem, 200),
		},
		System:    sysPrompt,
		User:      userPrompt,
		LatencyMS: time.Since(started).Milliseconds(),
	}
	if gateCtx != nil {
		trace.GateReturned = gateCtx.Sources
	}

	if llmErr != nil {
		trace.Err = llmErr.Error()
		WriteLLMTrace(trace)
		ch <- nodeCompletion{NodeID: sNode.ID, Err: fmt.Errorf("holmes LLM: %w", llmErr)}
		return
	}

	raw, extractErr := extractToolArgs(resp)
	if extractErr != nil {
		trace.Err = extractErr.Error()
		WriteLLMTrace(trace)
		ch <- nodeCompletion{NodeID: sNode.ID, Err: fmt.Errorf("holmes: %w", extractErr)}
		return
	}
	trace.Output = raw
	trace.TokensIn = resp.Usage.PromptTokens
	trace.TokensOut = resp.Usage.CompletionTokens
	WriteLLMTrace(trace)

	log.Printf("[dag] holmes iter %d output: %s", state.Iter, Text.TruncateLog(raw, 240))

	ch <- nodeCompletion{NodeID: sNode.ID, Result: raw, TokensIn: resp.Usage.PromptTokens, TokensOut: resp.Usage.CompletionTokens}
}

/*
 * loadHolmesState pulls the HolmesState out of the node's params. The
 * state is stored as a JSON-marshaled string so it survives the map[string]any
 * round-trip without losing typed fields.
 */
func loadHolmesState(n *Node) (*HolmesState, error) {
	if n == nil || n.Params == nil {
		return nil, fmt.Errorf("nil node or empty params")
	}
	raw, ok := n.Params["holmes_state"].(string)
	if !ok || raw == "" {
		return nil, fmt.Errorf("missing holmes_state on node")
	}
	var s HolmesState
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &s, nil
}

/*
 * saveHolmesState marshals the state and stores it on the node so the
 * next iteration can read it.
 */
func saveHolmesState(n *Node, s *HolmesState) error {
	if n.Params == nil {
		n.Params = make(map[string]any)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	n.Params["holmes_state"] = string(b)
	return nil
}

/*
 * parseHolmesOutput parses the raw LLM JSON into a holmesOutput struct.
 * Used by the scheduler when handling a Holmes node completion.
 */
func parseHolmesOutput(raw string) (*holmesOutput, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("empty holmes output")
	}
	var out holmesOutput
	if err := ParseLLMJSON(raw, &out); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if out.Reasoning == "" && out.Hypothesis == "" {
		return nil, fmt.Errorf("holmes output missing reasoning and hypothesis")
	}

	// Normalize all action formats into out.Actions.
	normalizeHolmesActions(&out)

	if out.Conclude && out.RCA == nil {
		// LLM said conclude but didn't fill RCA. Build a minimal one from
		// the hypothesis so the microplanner has something to work with.
		out.RCA = &RCAReport{
			RootCause:         out.Hypothesis,
			Evidence:          []string{},
			Confidence:        "low",
			SuggestedStrategy: "Investigate further; Holmes concluded without supporting evidence.",
		}
	}
	return &out, nil
}

/*
 * normalizeHolmesActions merges all possible action formats into out.Actions.
 * Handles three forms:
 *   1. New format: actions array already populated → use as-is
 *   2. Legacy single action: action object → wrap in Actions[0]
 *   3. multi_tool_use.parallel hallucination: action.tool == "multi_tool_use.parallel"
 *      with params.tool_uses → extract individual tools into Actions
 * After normalization, out.Action is nil and out.Actions is the canonical list.
 */
func normalizeHolmesActions(out *holmesOutput) {
	// If Actions already has content, use it as-is.
	if len(out.Actions) > 0 {
		out.Action = nil
		return
	}
	// No actions array — check for legacy single action.
	if out.Action == nil {
		return
	}
	// Check for multi_tool_use.parallel hallucination.
	if out.Action.Tool == "multi_tool_use.parallel" {
		if extracted := extractMultiToolActions(out.Action.Params); len(extracted) > 0 {
			out.Actions = extracted
			out.Action = nil
			log.Printf("[dag] holmes: normalized multi_tool_use.parallel into %d actions", len(extracted))
			return
		}
		// Couldn't parse tool_uses — drop the action entirely.
		log.Printf("[dag] holmes: multi_tool_use.parallel had unparseable tool_uses, dropping")
		out.Action = nil
		return
	}
	// Normal single action → wrap in slice.
	out.Actions = []holmesAction{*out.Action}
	out.Action = nil
}

/*
 * extractMultiToolActions parses the tool_uses array from a
 * multi_tool_use.parallel params blob. Returns nil if the format
 * doesn't match.
 */
func extractMultiToolActions(params map[string]any) []holmesAction {
	toolUses, ok := params["tool_uses"].([]any)
	if !ok {
		return nil
	}
	var actions []holmesAction
	for _, tu := range toolUses {
		m, ok := tu.(map[string]any)
		if !ok {
			continue
		}
		// Claude emits: {"recipient_name": "functions.<tool>", "parameters": {...}}
		recipientName, _ := m["recipient_name"].(string)
		toolName := strings.TrimPrefix(recipientName, "functions.")
		if toolName == "" {
			// Fallback: maybe {"tool": "<tool>", "params": {...}}
			toolName, _ = m["tool"].(string)
		}
		if toolName == "" {
			continue
		}
		toolParams, _ := m["parameters"].(map[string]any)
		if toolParams == nil {
			toolParams, _ = m["params"].(map[string]any)
		}
		if toolParams == nil {
			toolParams = map[string]any{}
		}
		actions = append(actions, holmesAction{Tool: toolName, Params: toolParams})
	}
	return actions
}

/*
 * assembleHolmesPrompt builds the user message for one Holmes iteration.
 * desc: Sections in order:
 *         1. Original Request — what the user asked for
 *         2. Problem — the symptom the reflector handed Holmes
 *         3. Investigation Log — every prior turn (Holmes prose + actions +
 *            observations), so deduction can build on past evidence
 *         4. Iteration counter + budget warning
 *         5. Crime Scene — surface-level worklog tail (the most recent
 *            timestamped events). Bounded, summarised. Holmes has to call
 *            tools to read deeper than what the worklog already records.
 *         6. Floor Plan — workspace file tree (3 levels) so Holmes knows
 *            where to look instead of guessing paths.
 *         7. Blueprint path — just the file path, not the content. Holmes
 *            can file_read it if the investigation needs it.
 *         8. Detective's Notebook — domain skill guidance (e.g. webdeveloper
 *            Debug Guidance). Methodology, not evidence.
 *         9. Available Tools — full intent-allowed tool list with schemas
 *
 *       NOTE on the intent parameter: this MUST be the resolved investigation
 *       intent (post-auto-inference), not trigger.Intent(). For chat queries
 *       trigger.Intent() returns IntentAuto which would filter every tool out
 *       of the prompt and leave Holmes guessing tool names from training
 *       memory. The scheduler holds the resolved intent and passes it through.
 */
func assembleHolmesPrompt(state *HolmesState, trigger Trigger, a *Agent, intent gates.Intent, gateCtx *ContextResponse) string {
	var sb strings.Builder

	sb.WriteString("## Original Request\n\n")
	sb.WriteString(formatTrigger(trigger))
	sb.WriteString("\n\n")

	sb.WriteString("## The Problem\n\n")
	sb.WriteString(state.Problem)
	sb.WriteString("\n\n")

	if len(state.History) > 0 {
		sb.WriteString("## Investigation Log So Far\n\n")
		for _, turn := range state.History {
			sb.WriteString(fmt.Sprintf("### Iteration %d\n\n", turn.Iter))
			if turn.Reasoning != "" {
				sb.WriteString("**Holmes:** ")
				sb.WriteString(turn.Reasoning)
				sb.WriteString("\n\n")
			}
			if turn.Hypothesis != "" {
				sb.WriteString(fmt.Sprintf("**Hypothesis:** %s\n\n", turn.Hypothesis))
			}
			// Render actions + observations (new multi-action format)
			if len(turn.Actions) > 0 {
				for i, act := range turn.Actions {
					params, _ := json.Marshal(act.Params)
					sb.WriteString(fmt.Sprintf("**Action %d:** `%s(%s)`\n", i+1, act.Tool, string(params)))
					if i < len(turn.Observations) && turn.Observations[i] != "" {
						sb.WriteString(fmt.Sprintf("**Observation %d:**\n```\n%s\n```\n", i+1, Text.TruncateLog(turn.Observations[i], 1500)))
					}
					sb.WriteString("\n")
				}
			} else if turn.Action != nil {
				// Legacy single-action format (in-flight investigations)
				params, _ := json.Marshal(turn.Action.Params)
				sb.WriteString(fmt.Sprintf("**Action:** `%s(%s)`\n\n", turn.Action.Tool, string(params)))
				if turn.Observation != "" {
					sb.WriteString("**Observation:**\n```\n")
					sb.WriteString(Text.TruncateLog(turn.Observation, 1500))
					sb.WriteString("\n```\n\n")
				}
			}
		}
	}

	if len(state.Hypotheses) > 0 {
		sb.WriteString("## Hypotheses Already Tested\n\n")
		sb.WriteString("Do NOT propose any of these again — name a different theory or conclude.\n\n")
		for _, h := range state.Hypotheses {
			sb.WriteString("- ")
			sb.WriteString(h)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// The crime scene — surface-level worklog tail. The most recent
	// timestamped events the system recorded. Holmes walks in and sees
	// THIS — the equivalent of the room as it appears on first inspection.
	// Anything deeper (file contents, full stderr, process state) requires
	// a tool call.
	if gateCtx != nil {
		if wl := gateCtx.Sources[SourceWorklog]; wl != "" {
			sb.WriteString("## The Crime Scene\n\n")
			sb.WriteString("*\"You know my methods, Watson. There is nothing like first-hand evidence.\"* What follows is the surface of the scene as the system recorded it over the last few minutes — the immediate sights and sounds, the timestamped events, the surface signals. The reflector's problem statement above is one witness's interpretation; THIS is the raw scene. Cross-check the witness against the scene, and pull out your tools to dig beneath the surface — read the files, query the processes, fetch the logs. The crime scene tells you WHERE to look. Your tools tell you WHAT IS THERE.\n\n```\n")
			sb.WriteString(wl)
			sb.WriteString("\n```\n\n")
		}
	}

	// The floor plan — workspace file tree so Holmes knows where to look
	// instead of guessing paths. Shallow (3 levels) to orient, not to
	// replace tool-based exploration.
	if gateCtx != nil {
		if tree := gateCtx.Sources[SourceWorkspaceTree]; tree != "" {
			sb.WriteString("## The Floor Plan\n\n")
			sb.WriteString("Project directory structure:\n\n```\n")
			sb.WriteString(tree)
			sb.WriteString("\n```\n\n")
		}
	}

	// Blueprint path — Holmes can file_read it if needed.
	if a != nil && a.cfg.MetadataDir != "" {
		sid := ""
		if trigger.SessionID != "" {
			sid = trigger.SessionID
		}
		if bpPath := latestBlueprintPath(a.cfg.MetadataDir, sid); bpPath != "" {
			sb.WriteString("## Blueprint\n\n")
			sb.WriteString(fmt.Sprintf("The project blueprint is at: `%s`\n", bpPath))
			sb.WriteString("Use file_read to examine it if you need to understand the intended project structure.\n\n")
		}
	}

	// The detective's notebook — domain skill guidance. Methodology, not
	// evidence. The webdeveloper Debug Guidance section (and equivalents
	// for other skills) lives here as a starting framework for failure
	// patterns Holmes has seen before.
	if gateCtx != nil {
		if dg := gateCtx.Sources[SourceSkillGuidance]; dg != "" {
			sb.WriteString("## The Detective's Notebook\n\n")
			sb.WriteString("*\"Singularity is almost invariably a clue. The more featureless and commonplace a crime is, the more difficult it is to bring it home.\"* Below is your training for this kind of system — known patterns and the procedure for narrowing each one down. Treat it as a starting framework, not a textbook. Holmes never blindly follows a procedure; he adapts when the evidence points elsewhere. If the pattern doesn't match, name a new hypothesis and pursue it.\n\n")
			sb.WriteString(dg)
			sb.WriteString("\n\n")
		}
	}

	sb.WriteString(fmt.Sprintf("## Iteration Budget\n\nThis is iteration %d of %d. ", state.Iter, state.MaxIter))
	if state.Iter >= state.MaxIter {
		sb.WriteString("This is your LAST iteration — you MUST conclude this turn, even with low confidence.\n\n")
	} else if state.Iter >= state.MaxIter-1 {
		sb.WriteString("You have one more iteration after this. Wrap up the investigation.\n\n")
	} else {
		sb.WriteString("Continue investigating, or conclude if the evidence is sufficient.\n\n")
	}

	// Available tools — filtered against the resolved investigation intent
	// (NOT trigger.Intent() — that returns IntentAuto for chat queries and
	// would filter every tool out, leaving Holmes with an empty list and
	// hallucinating tool names from training memory).
	if a != nil {
		sb.WriteString("## Available Tools\n\n")
		sb.WriteString("Use these for diagnostic READS only. Do NOT mutate state.\n\n")
		for _, name := range a.registry.List() {
			sk, ok := a.registry.Get(name)
			if !ok {
				continue
			}
			rank := a.intentRegistry.ResolveToolIntent(name, sk, nil)
			if rank > int(intent) {
				continue
			}
			sb.WriteString(fmt.Sprintf("- **%s**: %s — `%s`\n", name, sk.Description(), string(sk.Parameters())))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
