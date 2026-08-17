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
 * param: triggerID - the caller's own id for this run.
 * param: throttle - the tool throttle instance.
 * param: intent - the IGX intent level.
 * param: scope - resolved tool access scope (nil for full access).
 */
/*
 * finish is how a step ends, wherever it ran.
 * desc: A step runs here or on another machine, and the two paths were kept
 *       level with each other by hand. The remote one has been found short four
 *       times now — the scope, clearance and AllowTool checks; the envelope it
 *       did not parse; a target with no executor falling through to local
 *       execution; and the display hint, which the local path attached and it
 *       did not, so a panel never appeared for a step that ran elsewhere.
 *
 *       Each was fixed where it was found, and nothing stopped the next one. So
 *       everything a completion carries beyond its four fields is assembled
 *       here, once, and neither path can be given something the other is not.
 *
 *       It returns the completion rather than sending it. The send has to stay
 *       at the call site: the panic guard sends one completion unconditionally,
 *       which is only correct while nothing runs after a send, and
 *       TestNodeCompletionsAreTerminal holds that property by reading the sends
 *       where they are written.
 * param: n - the node that ran. Its Actions gain the tool's display hint.
 * param: result - what the tool returned, empty on failure.
 * param: body - the typed body, nil for a tool that returned a string.
 * param: err - why it failed, nil on success.
 * return: the completion to send.
 */
func (a *Agent) finish(n *Node, result string, body NodeBody, err error) nodeCompletion {
	// A hint is derived from the tool as this machine has it, so a step that ran
	// elsewhere gets one only when the same tool is registered here too. That is
	// the honest limit: without the tool there is nothing to ask.
	//
	// And a hint that names a PATH is only meaningful where the file is. The
	// panel opens it on the machine running the dashboard, so a path from a
	// step that ran elsewhere shows nothing — or, worse, shows this machine's
	// file of that name as though it were the other machine's. A hint carrying
	// inline content makes no such assumption and travels.
	//
	// This is the one thing the two paths do differently, and it is written
	// here rather than by one of them quietly not doing it.
	if err == nil {
		if skill, ok := a.registry.Get(n.ToolName); ok {
			hint := toolapi.GetDisplayHint(skill, n.Params, result)
			if hint != nil && hint.Path != "" && a.remoteFor(n) {
				log.Printf("[dag] node %s ran on %s; its panel names a path on that machine, so it is not shown here",
					n.ID, Text.TruncateLog(n.Target, 12))
				hint = nil
			}
			if hint != nil {
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
	return nodeCompletion{NodeID: n.ID, Result: result, Body: body, Err: err}
}

func (a *Agent) fireNode(ctx context.Context, n *Node, graph *Graph,
	budget *Budget, ch chan<- nodeCompletion, triggerID string,
	throttle *toolThrottle, intent gates.Intent, scope *ResolvedScope) {

	defer a.guardNodeCompletion("fireNode", n.ID, ch)

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
		ch <- a.finish(n, "", nil, fmt.Errorf("dependency injection failed: %w", err))
		return
	}

	// Direct-param validation: reject keys the tool's schema doesn't
	// declare (and whose schema forbids extras). Closes the silent-drop
	// class where the planner invents params like bash(cwd: ...).
	if skill, ok := a.registry.Get(n.ToolName); ok {
		if err := validateDirectParams(skill, n.Params); err != nil {
			ch <- a.finish(n, "", nil, err)
			return
		}
	}

	// A step naming another machine with no way to reach one is refused, not
	// run here. Falling through to local execution was defended as behaving
	// "exactly as before targets existed", which stopped being true the moment
	// a planner could name a machine: process_kill aimed at another host then
	// kills a process on this one, and nothing says so.
	//
	// A target equal to this node's own id means "here" and is not this case.
	// The refusal is an error rather than a tool failure because no choice of
	// parameters fixes it — remote execution is either configured or it is not,
	// and the run should say that rather than work around it.
	if n.Type == NodeTool && n.Target != "" && n.Target != a.cfg.NodeID && a.remoteExec == nil {
		log.Printf("[dag] node %s: step is for %s and no remote executor is configured", n.ID, Text.TruncateLog(n.Target, 12))
		unreachable := fmt.Errorf("step is for %q and this agent has no remote executor, so it was not run", n.Target)
		ch <- a.finish(n, "", nil, unreachable)
		return
	}

	// A node type that is not a tool keeps running here whatever its target
	// says: compute and the other LLM-bearing types run where the agent runs.
	// Said out loud because the target is being ignored, and silence about
	// ignoring an instruction reads as having followed it.
	if n.Type != NodeTool && n.Target != "" && n.Target != a.cfg.NodeID {
		log.Printf("[dag] node %s: %s runs where the agent runs; the target %s is ignored",
			n.ID, n.Type, Text.TruncateLog(n.Target, 12))
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
		// Three of executeToolNode's checks are asked here as well, because the
		// far end cannot ask them: it knows its own clearance and its own tool
		// list, and nothing about who asked on this side. The other three stay
		// below and are deliberately not repeated — the registry lookup and the
		// IGX gate are the RECEIVING machine's to make against its own state,
		// and the throttle protects this process's rate limits, which a call
		// running elsewhere does not spend.
		// Every one of these lines is written here or nowhere. The local path
		// audits four times over — a throttle refusal, a gate refusal, a
		// clearance refusal, and the call itself — and a call that leaves this
		// machine went through none of that code.
		auditRemote := func(result string, err error) {
			e := gates.AuditEntry{
				Tool:      n.ToolName,
				Params:    n.Params,
				Target:    n.Target,
				Username:  usernameOf(scope),
				TriggerID: triggerID,
				RunID:     actionRunID(ctx, graph, triggerID),
				Intent:    int(intent),
			}
			if err != nil {
				e.Error = err.Error()
			} else {
				e.Result = Text.TruncateLog(result, 500)
			}
			a.audit(e)
		}

		if err := scopeAllows(n.ToolName, scope); err != nil {
			auditRemote("", err)
			ch <- a.finish(n, "", nil, err)
			return
		}
		if err := a.validateTarget(n.Target); err != nil {
			log.Printf("[dag] node %s: invalid target %q: %v", n.ID, n.Target, err)
			auditRemote("", err)
			ch <- a.finish(n, "", nil, fmt.Errorf("invalid target %q: %w", n.Target, err))
			return
		}
		if err := a.checkClearance(ctx, n.ToolName, n.Params, usernameOf(scope)); err != nil {
			auditRemote("", err)
			ch <- a.finish(n, "", nil, err)
			return
		}
		// Asked last here as it is below, and after the target check for the same
		// reason the local path asks it after the gate: the rule may write to the
		// parameter map, so nothing that judges the parameters may run after it.
		// A refusal is the call's result rather than an error, so the model reads
		// why and does something else.
		if allow, reason := a.allowTool(ctx, ToolCallRequest{
			Trigger: triggerFrom(ctx, graph),
			Graph:   graph,
			Tool:    n.ToolName,
			Params:  n.Params,
			Target:  n.Target,
		}); !allow {
			ch <- a.finish(n, reason, nil, nil)
			return
		}
		log.Printf("[dag] remote exec %s (%s) -> %s", n.ID, n.ToolName, Text.TruncateLog(n.Target, 12))
		result, err := a.remoteExecute(ctx, RemoteRequest{
			Target:        n.Target,
			Tool:          n.ToolName,
			Params:        n.Params,
			Intent:        int(intent),
			CorrelationID: triggerID,
		})
		// Parse the envelope the far end returned, so a result that ran
		// elsewhere arrives in the same shape as one that ran here. Without it
		// every consumer that reads the typed body — the failure detector, the
		// field references, the coverage statement — sees nothing for a remote
		// step and reads absence as success. A far end that returned something
		// other than an envelope leaves the body nil, exactly as before.
		var body NodeBody
		if msg, ok := toolapi.ParseToolMessage(result); ok {
			body = toolMessageBody{msg: msg}
		}
		// The executor's error is the far end's, and it may not mention which
		// machine it came from — "dial tcp: i/o timeout" names nothing. The
		// step's whole point was the machine, so the failure says it.
		if err != nil {
			err = fmt.Errorf("step on %q failed: %w", n.Target, err)
		}
		auditRemote(result, err)
		ch <- a.finish(n, result, body, err)
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

	result, body, err := a.executeToolNode(ctx, n, graph, budget, n.ToolName, n.Params, triggerID, intent, scope)
	ch <- a.finish(n, result, body, err)
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
	// Body.Field missed. Distinguish "envelope not JSON" (a tool bug — degrade
	// gracefully to the raw string so a working pipeline doesn't break) from
	// "JSON valid but field absent" (a planner bug — fail loud).
	var probe any
	if json.Unmarshal([]byte(dep.Result), &probe) != nil {
		log.Printf("[dag] template on %s: dep %s result is not JSON, injecting full result (upstream tool bug, not rejecting)", owner, depID)
		return dep.Result, nil
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
 * param: triggerID - the caller's own id for this run.
 * param: intent - the IGX intent level.
 * param: scope - resolved tool access scope (nil for full access).
 * return: result string and error.
 */
func (a *Agent) executeToolNode(ctx context.Context, n *Node, graph *Graph, budget *Budget,
	toolName string, params map[string]any, triggerID string, intent gates.Intent, scope *ResolvedScope) (string, NodeBody, error) {

	if err := scopeAllows(toolName, scope); err != nil {
		return "", nil, err
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
			a.audit(gates.AuditEntry{
				Tool:      toolName,
				Username:  usernameOf(scope),
				TriggerID: triggerID,
				RunID:     actionRunID(ctx, graph, triggerID),
				Error:     err.Error(),
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
		a.audit(gates.AuditEntry{
			Tool:      toolName,
			Username:  usernameOf(scope),
			TriggerID: triggerID,
			RunID:     actionRunID(ctx, graph, triggerID),
			Error:     err.Error(),
			Intent:    int(intent),
			Impact:    impact,
		})
		return "", nil, err
	}

	// Clearance: check external authorization endpoint (if configured)
	if a.clearanceCheck != nil {
		if err := a.checkClearance(ctx, toolName, params, usernameOf(scope)); err != nil {
			a.audit(gates.AuditEntry{
				Tool:      toolName,
				Username:  usernameOf(scope),
				TriggerID: triggerID,
				RunID:     actionRunID(ctx, graph, triggerID),
				Error:     err.Error(),
				Intent:    int(intent),
				Impact:    impact,
			})
			return "", nil, err
		}
	}

	// The application's own rules, asked last so it can only narrow what the
	// gate already allowed, never widen it. A refusal is handed to the model as
	// the call's result, so it learns why instead of trying again. See
	// allowtool.go.
	// Target is left empty on purpose: reaching here means the call runs on this
	// machine, whatever the node happens to name.
	if allow, reason := a.allowTool(ctx, ToolCallRequest{
		Trigger: triggerFrom(ctx, graph),
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
			TriggerID:  triggerID,
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
			// Exempt from the dispatch cap. Every typed tool reaches this, not
			// only the one it was written for — see maxToolResultLen.
			isContextual = true
		}
	} else {
		result, err = skill.Execute(ctx, params)
	}

	// Audit
	entry := gates.AuditEntry{
		Tool:      toolName,
		Params:    params,
		Username:  usernameOf(scope),
		TriggerID: triggerID,
		RunID:     actionRunID(ctx, graph, triggerID),
		Intent:    int(intent),
		Impact:    impact,
	}
	if err != nil {
		entry.Error = err.Error()
	} else {
		entry.Result = Text.TruncateLog(result, 500)
	}
	a.audit(entry)

	// Record side-effect actions in event store for audit trail
	if a.eventStore != nil && impact > 0 {
		paramsJSON := ""
		if params != nil {
			if b, e := json.Marshal(params); e == nil {
				paramsJSON = string(b)
			}
		}
		a.storeAction(Action{
			ID:         fmt.Sprintf("act-%d", time.Now().UnixNano()),
			NodeID:     a.cfg.NodeID,
			Timestamp:  time.Now().Unix(),
			ActionType: toolName,
			Params:     paramsJSON,
			Result:     Text.TruncateLog(result, 500),
			RunID:      actionRunID(ctx, graph, triggerID),
			Intent:     int(intent),
			Impact:     impact,
		})
	}

	if err != nil {
		return "", nil, err
	}

	// The second of the five caps — see maxToolResultLen for the other four
	// and the order they apply in.
	//
	// isContextual is set by the typed branch above, so this skips every tool
	// implementing TypedExecutor, not only compute. It was written for compute,
	// whose envelope the scheduler unmarshals for graft instructions and which
	// truncation would corrupt; it now exempts every typed tool, and the next
	// thing to cut such a result is TruncateEvidence at synthesis.
	//
	// truncateToolResult keeps JSON envelopes valid by shrinking the longest
	// string field inside rather than byte-splicing. Byte-splicing a web_fetch
	// envelope used to corrupt it, so a downstream ${node.X.field} could not
	// parse what it referenced.
	if !isContextual && len(result) > maxToolResultLen {
		result = truncateToolResult(result, maxToolResultLen, Text.HeadTail)
	}

	return result, body, nil
}

/*
 * scopeAllows reports whether the principal behind this run may use a tool.
 * desc: The permission list belongs to whoever asked — a logged-in dashboard
 *       user, typically — so it has to be applied wherever the call is going.
 *       A machine at the far end enforces its own clearance and knows nothing
 *       about who asked here, so this is the one check that has no counterpart
 *       on the receiving side.
 *
 *       A nil scope is full access: a run with no principal, which is what an
 *       unattended investigation is.
 * param: toolName - the tool about to be called.
 * param: scope - the resolved permissions, or nil.
 * return: nil when the tool may be used.
 */
func scopeAllows(toolName string, scope *ResolvedScope) error {
	if scope == nil || scope.AllowedTools["*"] || scope.AllowedTools[toolName] {
		return nil
	}
	return fmt.Errorf("gate: %s not in user scope", toolName)
}

/*
 * usernameOf names the principal a scope belongs to, if any.
 * param: scope - the resolved permissions, or nil.
 * return: the username, empty when the run has no principal.
 */
func usernameOf(scope *ResolvedScope) string {
	if scope == nil {
		return ""
	}
	return scope.Username
}
