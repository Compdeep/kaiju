package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
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
 * desc: Applies per-tool throttle if the tool declares one. If the node's
 *       params contain ${node.<id>.field} placeholders, the dispatcher
 *       substitutes them from upstream node outputs first. Attaches tool
 *       display hints as NodeActions before sending completion.
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

	// Data-flow validation lives at the executive-output boundary
	// (validatePlanSteps in executive.go), not here. Architect-grafted
	// coder nodes (NodeCompute children of a compute(deep) parent)
	// legitimately use depends_on for sequencing while communicating via
	// files on disk — they don't need ${step.N.field} placeholders.
	// Validating at dispatch time blanket-rejected them; the executive
	// boundary is the right layer because that's where the validator's
	// failure mode (planner LLM under-wiring) actually originates.

	// Substitute ${node.<id>(.path)?} templates in params from dependency
	// outputs. planStepsToNodes already rewrote the planner's
	// ${step.N(.path)?} form to ${node.<id>(.path)?}, so by this point
	// every reference points at a concrete node id. Fails fast if the
	// dep hasn't resolved or the named field is absent — same recovery
	// chain handles that case.
	if err := substituteTemplates(n, graph); err != nil {
		log.Printf("[dag] node %s template substitution failed: %v", n.ID, err)
		ch <- nodeCompletion{NodeID: n.ID, Err: fmt.Errorf("dependency injection failed: %w", err)}
		return
	}

	// Direct-param validation: reject keys the tool's schema doesn't
	// declare (and whose schema forbids extras). Closes the silent-drop
	// class where the planner invents params like bash(cwd: ...).
	if skill, ok := a.registry.Get(n.ToolName); ok {
		if err := validateDirectParams(skill, n.Params); err != nil {
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
 * substituteTemplates resolves ${node.<id>(.path)?} placeholders in the
 * node's params from dependency outputs. Walks every string value in
 * params (including nested maps/arrays), replaces each match by looking
 * up the named dep node, extracting the named field via dot-path, and
 * substituting the value. Bare placeholders (the entire string IS the
 * placeholder) replace the param value with the raw extracted value;
 * embedded placeholders inside larger strings interpolate as text.
 *
 * Returns error if any dep is missing, has empty Result, or the field
 * is absent from a valid JSON Result. Tool-output that isn't valid JSON
 * gracefully degrades to the full Result string for non-bare cases.
 *
 * Bash failures whose Result is the bash_error JSON blob are treated as
 * legitimate dep output — the planner often chains on stderr to drive
 * the next step's diagnosis.
 */
func substituteTemplates(n *Node, graph *Graph) error {
	if n.Params == nil {
		return nil
	}
	var firstErr error
	walkParams(n.Params, func(s string) (any, bool) {
		// Special case: the WHOLE string is a single bare placeholder.
		// In that case, replace the param value with the extracted
		// value as-is (preserving its original type — string, number,
		// object, etc.) instead of stringifying.
		if m := nodeTemplateBareRe.FindStringSubmatch(s); m != nil {
			depID := m[1]
			field := m[2]
			val, err := resolveTemplateField(graph, depID, field, n.ID)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return s, false
			}
			log.Printf("[dag] inject %s ← node %s%s (%d bytes)", n.ID, depID, dotPrefix(field), len(fmt.Sprint(val)))
			return val, true
		}
		// Embedded placeholders inside a larger string: replace each
		// match with its string form.
		out := nodeTemplateRe.ReplaceAllStringFunc(s, func(match string) string {
			m := nodeTemplateRe.FindStringSubmatch(match)
			depID := m[1]
			field := m[2]
			val, err := resolveTemplateField(graph, depID, field, n.ID)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return match
			}
			return fmt.Sprint(val)
		})
		if out != s {
			return out, true
		}
		return s, false
	})
	return firstErr
}

// resolveTemplateField looks up dep node by ID, verifies it has a
// non-empty Result, and extracts the named field. Returns the extracted
// value (any-typed for bare matches) plus an error describing the exact
// failure mode if anything is wrong.
//
// owner is included in error messages so the recovery chain can name
// which step failed.
func resolveTemplateField(graph *Graph, depID, field, owner string) (any, error) {
	dep := graph.Get(depID)
	if dep == nil {
		return nil, fmt.Errorf("template on %s: dep node %s not found", owner, depID)
	}
	if dep.Result == "" {
		return nil, fmt.Errorf("template on %s: dep %s has empty result (%s)", owner, depID, dep.State)
	}
	if dep.State == StateFailed {
		log.Printf("[dag] template on %s: injecting from failed dep %s", owner, depID)
	}
	if field == "" {
		return dep.Result, nil
	}
	// Tolerant of upstream tool serialization bugs: if the Result is
	// non-JSON, fall back to returning the raw string so a working
	// pipeline doesn't break on a malformed envelope (the brace_json
	// dispatcher silent-fallback fix path). Probe up front so we can
	// distinguish "envelope is malformed" from "JSON valid but field
	// missing" — the former is a tool bug and degrades gracefully, the
	// latter is a planner bug and fails loud.
	var probe any
	if json.Unmarshal([]byte(dep.Result), &probe) != nil {
		log.Printf("[dag] template on %s: dep %s result is not JSON, injecting full result (upstream tool bug, not rejecting)", owner, depID)
		return dep.Result, nil
	}
	val, err := extractJSONFieldAny(dep.Result, field)
	if err != nil {
		return nil, fmt.Errorf("template on %s: field %q absent in dep %s (%w)", owner, field, depID, err)
	}
	return val, nil
}

// nodeTemplateRe matches embedded ${node.<id>(.path)?} placeholders
// anywhere within a string. nodeTemplateBareRe enforces that the WHOLE
// string is a single placeholder (no surrounding text), used to decide
// whether to do a value-preserving substitution or a string-form
// interpolation.
var (
	nodeTemplateRe     = regexp.MustCompile(`\$\{node\.([a-zA-Z0-9_-]+)(?:\.([^}]+))?\}`)
	nodeTemplateBareRe = regexp.MustCompile(`^\$\{node\.([a-zA-Z0-9_-]+)(?:\.([^}]+))?\}$`)
)

// walkParams recursively visits every string-typed leaf in v and lets
// fn rewrite it. fn returns (newValue, replaced) — when replaced is
// true and newValue is not a string, the leaf is replaced with the
// non-string value as-is (preserving type for bare-placeholder
// substitution). Maps and slices are walked; other types untouched.
func walkParams(v any, fn func(string) (any, bool)) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok {
				if newVal, ok := fn(s); ok {
					x[k] = newVal
				}
			} else {
				walkParams(val, fn)
			}
		}
	case []any:
		for i, val := range x {
			if s, ok := val.(string); ok {
				if newVal, ok := fn(s); ok {
					x[i] = newVal
				}
			} else {
				walkParams(val, fn)
			}
		}
	}
}

// dotPrefix is a one-liner: "" → "", "x" → ".x". Pure log cosmetic.
func dotPrefix(s string) string {
	if s == "" {
		return ""
	}
	return "." + s
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
		// Cards live on the graph (per-investigation).
		var activeCards []string
		if graph != nil {
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
	// truncateToolResult keeps JSON envelopes valid by shrinking the
	// longest string field inside rather than byte-splicing. For non-JSON
	// output it falls back to head+tail (unchanged from before). Byte-
	// splicing a web_fetch JSON used to corrupt the envelope so downstream
	// ${node.X.field} substitution couldn't parse it — this fixes that
	// without giving up the LLM-friendly truncation behaviour.
	if !isContextual && len(result) > maxToolResultLen {
		result = truncateToolResult(result, maxToolResultLen, Text.HeadTail)
	}

	return result, nil
}
