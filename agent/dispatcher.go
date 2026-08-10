package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/toolapi"
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

	// Which machine this step runs on.
	//
	// Only asked where there is more than one answer. An application that
	// supplied no executor runs everything where the agent runs, so a step that
	// names no machine is not an omission — it is the only possibility, and
	// refusing it would refuse every step. An application that CAN send work
	// elsewhere has to be told, because "here" is then a choice someone made
	// rather than the only option, and a step that names nothing is a step
	// nobody decided the location of.
	//
	// Tool nodes only. Compute and the reflection types run where the agent
	// runs whatever anyone writes.
	if n.Type == NodeTool && a.remoteExec != nil {
		if skill, ok := a.registry.Get(n.ToolName); ok {
			// "self" is what a planner writes for this machine without knowing
			// its id. Resolved before anything reads the target.
			if n.Target == selfTarget {
				n.Target = a.cfg.NodeID
			}
			if toolapi.RequiresTarget(skill) {
				if n.Target == "" {
					log.Printf("[dag] node %s: %q needs a target machine; rejecting empty target",
						n.ID, n.ToolName)
					ch <- nodeCompletion{NodeID: n.ID, Err: fmt.Errorf(
						"tool %q requires step.target — name a machine, or %q for this one",
						n.ToolName, selfTarget)}
					return
				}
			} else if n.Target != "" {
				log.Printf("[dag] node %s: %q takes no target; stripping %q",
					n.ID, n.ToolName, n.Target)
				n.Target = ""
			}
		}
	}

	// Remote execution: the planner named a machine and the embedding
	// application supplied an executor, so hand the call over rather than
	// running it here. Local throttling is deliberately skipped — the cooldown
	// protects THIS process's rate limits, and the work is not happening in
	// this process.
	//
	// Whatever authorisation the far end applies is its own business; nothing
	// here assumes the receiving side trusts the intent travelling with the
	// request.
	if a.remoteFor(n) {
		if err := a.validateTarget(n.Target); err != nil {
			log.Printf("[dag] node %s: invalid target %q: %v", n.ID, n.Target, err)
			ch <- nodeCompletion{NodeID: n.ID, Err: fmt.Errorf("invalid target %q: %w", n.Target, err)}
			return
		}
		log.Printf("[dag] remote exec %s (%s) -> %s", n.ID, n.ToolName, Text.TruncateLog(n.Target, 12))
		result, err := a.remoteExec.Execute(ctx, RemoteRequest{
			Target:        n.Target,
			Tool:          n.ToolName,
			Params:        n.Params,
			Intent:        int(intent),
			CorrelationID: alertID,
		})
		ch <- nodeCompletion{NodeID: n.ID, Result: result, Err: err}
		return
	}

	// Enforce per-tool cooldown before executing
	if skill, ok := a.registry.Get(n.ToolName); ok {
		cooldown := toolapi.GetThrottle(skill)
		if cooldown > 0 {
			throttle.waitThrottle(ctx, n.ToolName, cooldown)
		}
	}

	if len(n.Params) > 0 {
		paramJSON, _ := json.Marshal(n.Params)
		log.Printf("[dag] exec %s (%s) params=%s", n.ID, n.ToolName, Text.TruncateLog(string(paramJSON), 200))
	}

	result, body, err := a.executeToolNode(ctx, n, graph, budget, n.ToolName, n.Params, alertID, intent, scope)

	// Attach tool actions to the node before completion so they're
	// included in the node event when SetResult emits it.
	if err == nil {
		if skill, ok := a.registry.Get(n.ToolName); ok {
			if hint := toolapi.GetDisplayHint(skill, n.Params, result); hint != nil {
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
		Body:   body,
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
	// A ${step.N…} reference is rewritten to ${node.<id>…} when the plan is
	// finalised, so one that survives to here was never rewritten — grafted by a
	// stage that bypassed finalisation, most likely. Say so: the regexes below
	// match only the node form, so it would otherwise be left in the parameter
	// as literal text and handed to the tool.
	for _, ref := range FindRefs(n.Params) {
		if ref.Type == "step" {
			return fmt.Errorf("template %s on %s: a step reference reached fire time, so the rewrite to ${node.<id>…} was missed", ref.Raw, n.ID)
		}
	}

	// 3. Resolved once per dependency rather than once per reference: a node
	// referenced five times was parsed five times.
	resolved := make(map[string]any, 4)
	var firstErr error
	walkParams(n.Params, func(s string) (any, bool) {
		// Special case: the WHOLE string is a single bare placeholder.
		// In that case, replace the param value with the extracted
		// value as-is (preserving its original type — string, number,
		// object, etc.) instead of stringifying.
		if m := nodeTemplateBareRe.FindStringSubmatch(s); m != nil {
			depID := m[1]
			field := m[2]
			val, err := resolveTemplateFieldCached(graph, resolved, depID, field, n.ID)
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
			val, err := resolveTemplateFieldCached(graph, resolved, depID, field, n.ID)
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
func resolveTemplateFieldCached(graph *Graph, cache map[string]any, depID, field, owner string) (any, error) {
	key := depID + "\x00" + field
	if v, ok := cache[key]; ok {
		return v, nil
	}
	v, err := resolveTemplateField(graph, depID, field, owner)
	if err != nil {
		return nil, err
	}
	cache[key] = v
	return v, nil
}

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
		// No path: the whole result. Ask the body for it, so a tool that
		// returns an envelope gives its payload rather than the line of text
		// the envelope renders for a human. Reading dep.Result directly used to
		// give that line, because SetBody stores the evidence text there — so a
		// step wanting the data of the step before it received prose.
		//
		// Parsed when what comes back is text, so a step receives the object its
		// predecessor produced rather than the text of one: a tool given a
		// string where it expected a map fails on the far side, where the cause
		// is no longer visible.
		if dep.Body != nil {
			if v, ok := dep.Body.Field(""); ok {
				if text, isText := v.(string); isText {
					return parseResultForTemplate(text), nil
				}
				return v, nil
			}
		}
		return parseResultForTemplate(dep.Result), nil
	}
	// Resolve the dot-path through the node's typed body — the single field
	// access primitive. RawTextBody (the default for tools today) parses its
	// JSON and walks the path, exactly as before; typed bodies may read their
	// own fields. A hit returns the typed value.
	if dep.Body != nil {
		if v, ok := dep.Body.Field(field); ok {
			return v, nil
		}
	}
	// Body.Field missed, so the field cannot be read. Both ways of missing are
	// an error, and the message says which one happened.
	//
	// The result not being JSON used to be forgiven here: it was logged, and the
	// whole result was injected in place of the field. That hands a tool the
	// entire output of the step before it where one field was asked for, and
	// nothing says so until whatever reads it misbehaves — far from the step
	// that caused it. A step asked for a field; if there is no field, the step
	// is wrong and should say so where it is written.
	var probe any
	if json.Unmarshal([]byte(dep.Result), &probe) != nil {
		return nil, fmt.Errorf("template on %s: field %q was asked of dep %s, whose result is not JSON and so has no fields",
			owner, field, depID)
	}
	return nil, fmt.Errorf("template on %s: field %q absent in dep %s", owner, field, depID)
}

/*
 * parseResultForTemplate returns a node's result as a value.
 * desc: Parsed when it looks like JSON, the raw string otherwise — so a tool
 *       that returns prose is injected as prose, and one that returns an object
 *       is injected as an object.
 * param: s - the node's result.
 * return: the value to inject.
 */
func parseResultForTemplate(s string) any {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return s
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return s
	}
	return parsed
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
 *       in the event store. Every tool is called the same way; the run state a
 *       tool like compute needs travels on the ctx.
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
	toolName string, params map[string]any, alertID string, intent gates.Intent, scope *ResolvedScope) (string, NodeBody, error) {

	// Scope check: reject tools not in the user's scope (defense-in-depth)
	// Wildcard "*" in AllowedTools means all tools allowed.
	if scope != nil && !scope.AllowedTools["*"] && !scope.AllowedTools[toolName] {
		return "", nil, fmt.Errorf("gate: %s not in user scope", toolName)
	}

	skill, ok := a.registry.Get(toolName)
	if !ok {
		return "", nil, fmt.Errorf("unknown tool: %s", toolName)
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
			return "", nil, err
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
		return "", nil, err
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
			return "", nil, err
		}
	}

	// The application's own rules, asked last so it can only narrow what the
	// gate already allowed, never widen it. A refusal is handed to the model as
	// the call's result, so it learns why instead of trying again. See
	// allowtool.go.
	if allow, reason := a.allowTool(ctx, ToolCallRequest{
		Trigger: triggerOf(graph),
		Graph:   graph,
		Tool:    toolName,
		Params:  params,
	}); !allow {
		return reason, nil, nil
	}

	// Execute. A tool that returns a ToolMessage takes the typed path; anything
	// else returns a string. A structured envelope is pipeline data — a compute
	// plan carries follow_up graft instructions — and must not be truncated.
	var result string
	var body NodeBody
	var err error
	isContextual := false

	// Build the run state once, before choosing a path, and put it on the ctx.
	// It used to be built inside the contextual branch only, which meant a tool
	// that returned a typed message could never receive it — the typed branch
	// wins the fork below, so its graph and budget were simply never built. Now
	// the two questions are separate: the branch decides what a tool returns,
	// the ctx carries what it can reach. See WithExecContext.
	var ec *ExecuteContext
	if n != nil {
		var activeCards []string
		if graph != nil {
			activeCards = graph.ActiveCards
		}
		cards, cardNames := a.resolveComputeSkillCards(activeCards)
		ec = &ExecuteContext{
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
		ec.cardNames = cardNames
		ctx = WithExecContext(ctx, ec)
	}

	if tx, ok := skill.(toolapi.TypedExecutor); ok {
		// Typed path: the tool returns a ToolMessage directly — no JSON round-trip.
		// The node records which cards contributed guidance, so a trace shows
		// what this run was coding against. Only the tools that consume the
		// cards claim them — every tool has an ExecuteContext, and marking them
		// all would say a file_read was guided by the coder doctrine.
		if ec != nil && len(ec.cardNames) > 0 && (toolName == "compute" || toolName == "edit_file") {
			n.Skills = ec.cardNames
		}
		var msg toolapi.ToolMessage
		if msg, err = tx.ExecuteTyped(ctx, params); err == nil {
			body = toolMessageBody{msg: msg}
			result = msg.JSON()
			isContextual = true // structured envelope — exempt from truncation
		}
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
		a.eventStore.InsertAction(Action{
			ID:         fmt.Sprintf("act-%d", time.Now().UnixNano()),
			NodeID:     a.cfg.NodeID,
			Timestamp:  time.Now().Unix(),
			ActionType: toolName,
			Params:     paramsJSON,
			Result:     Text.TruncateLog(result, 500),
			RunID:      runIDOf(graph, alertID),
			Intent:     int(intent),
			Impact:     impact,
		})
	}

	if err != nil {
		return "", nil, err
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

	return result, body, nil
}
