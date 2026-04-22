package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/gates"
	"github.com/Compdeep/kaiju/internal/agent/tools"
	"github.com/Compdeep/kaiju/internal/compat/store"
)

/*
 * toolThrottle serializes concurrent calls to the same tool so that
 * external API rate limits are respected.
 * desc: Each tool gets its own mutex and last-fire timestamp. Goroutines
 *       calling waitThrottle block until the declared cooldown has elapsed
 *       since the previous call.
 */
type toolThrottle struct {
	mu    sync.Mutex
	gates map[string]*throttleGate
}

/*
 * throttleGate is a per-tool mutex and timestamp for throttle enforcement.
 * desc: Serializes calls to a single tool with a minimum time gap between calls.
 */
type throttleGate struct {
	mu       sync.Mutex
	lastFire time.Time
}

/*
 * newToolThrottle creates a new toolThrottle.
 * desc: Initializes the throttle with an empty gate map.
 * return: pointer to the new toolThrottle.
 */
func newToolThrottle() *toolThrottle {
	return &toolThrottle{gates: make(map[string]*throttleGate)}
}

/*
 * gate returns the throttle gate for a tool, creating one if needed.
 * desc: Thread-safe lazy initialization of per-tool throttle gates.
 * param: name - the tool name.
 * return: pointer to the throttleGate for this tool.
 */
func (st *toolThrottle) gate(name string) *throttleGate {
	st.mu.Lock()
	defer st.mu.Unlock()
	g, ok := st.gates[name]
	if !ok {
		g = &throttleGate{}
		st.gates[name] = g
	}
	return g
}

/*
 * waitThrottle blocks until the tool's cooldown period has elapsed.
 * desc: Acquires the per-tool mutex, checks elapsed time since last fire,
 *       sleeps for the remaining cooldown if needed, then records the new
 *       fire time. Returns early if context is cancelled.
 * param: ctx - context for cancellation.
 * param: toolName - the tool to throttle.
 * param: cooldown - minimum duration between calls.
 * return: duration since the last fire time after waiting.
 */
func (st *toolThrottle) waitThrottle(ctx context.Context, toolName string, cooldown time.Duration) time.Duration {
	g := st.gate(toolName)
	g.mu.Lock()
	defer g.mu.Unlock()

	since := time.Since(g.lastFire)
	if since < cooldown {
		wait := cooldown - since
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return time.Since(g.lastFire)
		}
	}
	g.lastFire = time.Now()
	return time.Since(g.lastFire)
}

/*
 * fireNode runs a single tool node and sends the result on ch.
 * desc: Applies per-tool throttle if the tool declares one. If the node
 *       has ParamRefs, dependency injection resolves them first. Attaches
 *       tool display hints as NodeActions before sending completion.
 * param: ctx - context for execution.
 * param: n - the Node to execute.
 * param: graph - the investigation graph.
 * param: budget - the execution budget.
 * param: ch - channel to send the completion result.
 * param: alertID - the investigation alert ID.
 * param: throttle - the tool throttle instance.
 * param: intent - the IGX intent level.
 * param: scope - resolved tool access scope (nil for full access).
 */
func (a *Agent) fireNode(ctx context.Context, n *Node, graph *Graph,
	budget *Budget, ch chan<- nodeCompletion, alertID string,
	throttle *toolThrottle, intent gates.Intent, scope *ResolvedScope) {

	// Tag every node with the investigation's active skills so the
	// frontend can show which skills guided this run. Skills are
	// investigation-wide (set by preflight), not tool-specific.
	if n.Skills == nil && graph != nil && len(graph.ActiveCards) > 0 {
		n.Skills = graph.ActiveCards
	}

	// Resolve param_refs from dependency outputs before execution.
	// Fails fast if dep not resolved, field missing, or value empty.
	if len(n.ParamRefs) > 0 {
		for paramName, ref := range n.ParamRefs {
			log.Printf("[dag] inject %s.%s ← %s.%s", n.ID, paramName, ref.NodeID, ref.Field)
		}
		if err := resolveInjections(n, graph); err != nil {
			log.Printf("[dag] node %s injection failed: %v", n.ID, err)
			ch <- nodeCompletion{NodeID: n.ID, Err: fmt.Errorf("dependency injection failed: %w", err)}
			return
		}
		// Log resolved values for debugging
		for paramName := range n.ParamRefs {
			log.Printf("[dag] inject %s.%s = %v", n.ID, paramName, Text.TruncateLog(fmt.Sprint(n.Params[paramName]), 120))
		}
	}

	// Direct-param validation: reject keys the tool's schema doesn't
	// declare (and whose schema forbids extras). Closes the silent-drop
	// class where the planner invents params like bash(cwd: ...). Keys
	// injected via ParamRefs are excluded — those get validated at the
	// injection site by validateParamRef inside resolveInjections.
	if skill, ok := a.registry.Get(n.ToolName); ok {
		if err := validateDirectParams(skill, n.Params, n.ParamRefs); err != nil {
			ch <- nodeCompletion{NodeID: n.ID, Err: err}
			return
		}
	}

	// Enforce per-tool cooldown before executing
	if skill, ok := a.registry.Get(n.ToolName); ok {
		cooldown := tools.GetThrottle(skill)
		if cooldown > 0 {
			throttle.waitThrottle(ctx, n.ToolName, cooldown)
		}
	}

	if len(n.Params) > 0 {
		paramJSON, _ := json.Marshal(n.Params)
		log.Printf("[dag] exec %s (%s) params=%s", n.ID, n.ToolName, Text.TruncateLog(string(paramJSON), 200))
	}

	result, err := a.executeToolNode(ctx, n, graph, budget, n.ToolName, n.Params, alertID, intent, scope)

	// Attach tool actions to the node before completion so they're
	// included in the node event when SetResult emits it.
	if err == nil {
		if skill, ok := a.registry.Get(n.ToolName); ok {
			if hint := tools.GetDisplayHint(skill, n.Params, result); hint != nil {
				n.Actions = append(n.Actions, NodeAction{
					Type:    "panel_show",
					Plugin:  hint.Plugin,
					Title:   hint.Title,
					Path:    hint.Path,
					Content: hint.Content,
					Mime:    hint.Mime,
					Line:    hint.Line,
				})
			}
		}
	}

	ch <- nodeCompletion{
		NodeID: n.ID,
		Result: result,
		Err:    err,
	}
}

/*
 * resolveInjections populates node params from dependency outputs.
 * desc: For each ParamRef, looks up the dependency node, verifies it resolved
 *       successfully, parses its result as JSON, extracts a field by dot-path,
 *       optionally applies a template, and injects the value into n.Params.
 *       Returns error if any step fails (node not found, not resolved, invalid
 *       JSON, missing field, or empty value).
 * param: n - the node whose params to populate.
 * param: graph - the investigation graph for dependency lookup.
 * return: error if injection fails for any param.
 */
func resolveInjections(n *Node, graph *Graph) error {
	for paramName, ref := range n.ParamRefs {
		dep := graph.Get(ref.NodeID)
		if dep == nil {
			return fmt.Errorf("param_ref %q: dependency node %s not found", paramName, ref.NodeID)
		}
		// A node's Result is the data to inject regardless of whether the node
		// ended in StateResolved or StateFailed. Bash nodes that exit non-zero
		// still capture stderr/stdout as Result (a JSON bash_error blob), and
		// that output is often EXACTLY what the planner wants to chain on —
		// e.g. running a failing service to harvest its log. Rejecting failed
		// deps blind-drops the most diagnostically useful data we have.
		//
		// We only bail when Result is literally empty (StateSkipped via
		// cascade-prune, or an unexpected early return).
		if dep.Result == "" {
			// Context params (file reads) degrade to a placeholder; required
			// params fail the node.
			if strings.HasPrefix(paramName, "context.") {
				if n.Params == nil {
					n.Params = make(map[string]any)
				}
				n.Params[paramName] = fmt.Sprintf("[file unavailable: %s]", dep.Tag)
				log.Printf("[dag] param_ref %q: dep %s empty (%s), placeholder injected", paramName, ref.NodeID, dep.State)
				continue
			}
			return fmt.Errorf("param_ref %q: dep %s empty (%s)", paramName, ref.NodeID, dep.State)
		}
		if dep.State == StateFailed {
			log.Printf("[dag] param_ref %q: injecting from failed dep %s", paramName, ref.NodeID)
		}

		var value string
		if ref.Field == "" {
			// Empty field = use the entire result as-is (e.g., file_read raw text)
			value = dep.Result
		} else {
			// Validate presence of the named field in the dep's Result
			// before substituting. The prior silent-fallback (using the
			// full Result when the field was absent) was the brace_json
			// bug: file_write received compute's metadata blob instead
			// of a non-existent .output field, clobbering the target.
			if err := validateParamRef(paramName, ref, dep.Result); err != nil {
				return err
			}
			value, _ = extractJSONField(dep.Result, ref.Field)
		}
		// Empty values are rejected — they'd produce invalid tool parameters.
		if value == "" {
			return fmt.Errorf("param_ref %q: field %q from node %s resolved to empty", paramName, ref.Field, ref.NodeID)
		}

		if ref.Template != "" {
			value = strings.ReplaceAll(ref.Template, "{{value}}", value)
		}

		if n.Params == nil {
			n.Params = make(map[string]any)
		}
		n.Params[paramName] = value
	}
	return nil
}

/*
 * extractJSONField extracts a value from a JSON string by dot-path.
 * desc: Supports nested objects ("host.name") and arrays ("ips.0").
 *       Returns the value as a string (primitives as-is, objects/arrays as JSON).
 * param: jsonStr - the JSON string to parse.
 * param: fieldPath - dot-separated path to the desired field.
 * return: the extracted value as a string, or error.
 */

/*
 * executeToolNode runs a tool through the IGX gate pipeline.
 * desc: Performs scope check, rate limit check, IGX triad check (impact <=
 *       min(intent, clearance, scope_cap)), optional external clearance check,
 *       then executes the tool. Audits all attempts and records side-effects
 *       in the event store. Tools implementing ContextualExecutor are invoked
 *       via ExecuteWithContext with a populated ExecuteContext; others fall
 *       through to plain Execute.
 * param: ctx - context for execution.
 * param: n - the node being executed (may be nil for actuator path).
 * param: graph - the investigation graph (may be nil for actuator path).
 * param: budget - the execution budget (may be nil for actuator path).
 * param: toolName - the name of the tool to execute.
 * param: params - the tool parameters.
 * param: alertID - the investigation alert ID.
 * param: intent - the IGX intent level.
 * param: scope - resolved tool access scope (nil for full access).
 * return: result string and error.
 */
func (a *Agent) executeToolNode(ctx context.Context, n *Node, graph *Graph, budget *Budget,
	toolName string, params map[string]any, alertID string, intent gates.Intent, scope *ResolvedScope) (string, error) {

	// Scope check: reject tools not in the user's scope (defense-in-depth)
	// Wildcard "*" in AllowedTools means all tools allowed.
	if scope != nil && !scope.AllowedTools["*"] && !scope.AllowedTools[toolName] {
		return "", fmt.Errorf("gate: %s not in user scope", toolName)
	}

	skill, ok := a.registry.Get(toolName)
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}

	// Resolve the tool's effective impact via the intent registry (DB
	// override wins, falls back to tool.Impact() default).
	impact := a.intentRegistry.ResolveToolIntent(toolName, skill, params)
	// Gate: rate limit (rank-0 tools exempt — reading local files should not be throttled)
	if impact > 0 {
		if err := a.gate.CheckRateLimit(); err != nil {
			a.gate.Audit(gates.AuditEntry{
				Tool:    toolName,
				AlertID: alertID,
				Error:   err.Error(),
			})
			return "", err
		}
	}

	// Ensure params is not nil
	if params == nil {
		params = make(map[string]any)
	}

	// Gate: IGX triad check with scope cap — impact <= min(intent, clearance, scope_cap)
	scopeCap := -1
	if scope != nil {
		if cap, ok := scope.MaxImpact[toolName]; ok {
			scopeCap = cap
		}
	}
	if err := a.gate.CheckTriadWithScope(intent, toolName, impact, scopeCap); err != nil {
		a.gate.Audit(gates.AuditEntry{
			Tool:    toolName,
			AlertID: alertID,
			Error:   err.Error(),
			Intent:  int(intent),
			Impact:  impact,
		})
		return "", err
	}

	// Clearance: check external authorization endpoint (if configured)
	if a.clearanceCheck != nil {
		username := ""
		if scope != nil {
			username = scope.Username
		}
		if err := a.clearanceCheck.Check(ctx, toolName, params, username); err != nil {
			a.gate.Audit(gates.AuditEntry{
				Tool:    toolName,
				AlertID: alertID,
				Error:   err.Error(),
				Intent:  int(intent),
				Impact:  impact,
			})
			return "", err
		}
	}

	// Execute — tools implementing ContextualExecutor get a rich context
	// built from scheduler-held state; others fall through to plain Execute.
	// Contextual results are structured pipeline data (e.g. compute plans
	// with follow_up graft instructions) and must not be truncated.
	var result string
	var err error
	isContextual := false
	if cx, ok := skill.(ContextualExecutor); ok && n != nil {
		isContextual = true
		// Resolve classifier-active skills into per-role guidance sections.
		// Compute uses this; other contextual tools may ignore it.
		// Cards live on the graph (per-investigation), with fallback to the
		// legacy agent field for safety.
		activeCards := a.activeCards
		if graph != nil && len(graph.ActiveCards) > 0 {
			activeCards = graph.ActiveCards
		}
		cards, names := a.resolveComputeSkillCards(activeCards)
		if len(names) > 0 {
			n.Skills = names
		}
		ec := &ExecuteContext{
			Ctx:        ctx,
			Node:       n,
			Graph:      graph,
			Budget:     budget,
			LLM:        a.llm,
			Executor:   a.executor,
			Workspace:  a.cfg.Workspace,
			AlertID:    alertID,
			Intent:     intent,
			SkillCards: cards,
		}
		result, err = cx.ExecuteWithContext(ec, params)
	} else {
		result, err = skill.Execute(ctx, params)
	}

	// Audit
	entry := gates.AuditEntry{
		Tool:    toolName,
		Params:  params,
		AlertID: alertID,
		Intent:  int(intent),
		Impact:  impact,
	}
	if err != nil {
		entry.Error = err.Error()
	} else {
		entry.Result = Text.TruncateLog(result, 500)
	}
	a.gate.Audit(entry)

	// Record side-effect actions in event store for audit trail
	if a.eventStore != nil && impact > 0 {
		paramsJSON := ""
		if params != nil {
			if b, e := json.Marshal(params); e == nil {
				paramsJSON = string(b)
			}
		}
		a.eventStore.InsertAction(store.Action{
			ID:              fmt.Sprintf("act-%d", time.Now().UnixNano()),
			NodeID:          a.cfg.NodeID,
			Timestamp:       time.Now().Unix(),
			ActionType:      toolName,
			Params:          paramsJSON,
			Result:          Text.TruncateLog(result, 500),
			InvestigationID: alertID,
			Intent:          int(intent),
			Impact:          impact,
		})
	}

	if err != nil {
		return "", err
	}

	// Truncate large results for normal tools. Contextual tools (compute)
	// return structured pipeline data that the scheduler unmarshals for
	// graft instructions — truncating would corrupt the JSON and silently
	// break the graft.
	//
	// Head+tail preserves both the beginning (context, file structure) and
	// the end (error lines, tracebacks, summaries) — the LLM was losing the
	// most diagnostic part of every long result before this.
	if !isContextual && len(result) > maxToolResultLen {
		result = Text.HeadTail(result, maxToolResultLen*2/3, maxToolResultLen/3,
			"\n... (middle truncated at 4KB cap; use start_line, grep, or tail to read the missing portion) ...\n")
	}

	return result, nil
}
