package agent

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

const maxToolResultLen = 4096

/*
 * systemPrompt returns the base system prompt for the ReAct loop.
 * desc: Composes the soul prompt with role description, ReAct role prompt,
 *       capability card context, and fleet section.
 * return: the fully composed system prompt string.
 */
func (a *Agent) systemPrompt() string {
	cardContext := ""
	if len(a.activeCards) > 0 {
		cardContext = "\n\n" + a.capabilities.ComposeBodies(a.activeCards)
	}
	rolePrompt := fmt.Sprintf("You are an agent on node %s.\n%s\n\n%s%s%s",
		a.cfg.NodeID, roleDescription(a.cfg.NodeRole), defaultReactRolePrompt, cardContext, a.fleetSection())
	return ComposeSystemPrompt(a.soulPrompt, rolePrompt)
}

/*
 * investigateReAct runs the ReAct loop for a single trigger.
 * desc: Executes a turn-based reasoning loop: classify capabilities, build
 *       messages with history, get tool definitions, derive intent, then
 *       iterate LLM calls and tool executions until the LLM stops or
 *       max turns is reached. Supports interjections between turns.
 * param: ctx - context for the investigation.
 * param: trigger - the investigation trigger.
 */
func (a *Agent) investigateReAct(ctx context.Context, trigger Trigger) {
	log.Printf("[agent] investigating: type=%s alert=%s source=%s",
		trigger.Type, trigger.AlertID, trigger.Source)

	startTime := time.Now()

	// Classify capabilities for this query (optional — disabled by default)
	if a.cfg.ClassifierEnabled && len(a.capabilities) > 0 {
		a.activeCards = a.classifyCapabilities(ctx, formatTrigger(trigger))
		log.Printf("[agent] classified: %v", a.activeCards)
	}

	// Build initial messages with conversation history
	messages := BuildMessagesWithHistory(
		a.systemPrompt(),
		formatTrigger(trigger),
		trigger.History,
	)

	// Get tool definitions (filtered by semantic routing if enabled)
	relevant := a.relevantTools(ctx, formatTrigger(trigger), trigger.Scope)
	toolDefs := a.registry.ToolDefsForNames(relevant)
	if len(toolDefs) == 0 {
		log.Printf("[agent] no tools registered, skipping investigation")
		return
	}

	// IGX: derive intent once for the entire investigation. The ReAct path
	// has no planner to infer from, so auto falls back to the registry's
	// default rank.
	intent := trigger.Intent()
	if intent == gates.IntentAuto {
		intent = gates.Intent(a.intentRegistry.DefaultRank())
	}
	log.Printf("[agent] intent: %s", intent)

	// ReAct loop
	for turn := 0; turn < a.cfg.MaxTurns; turn++ {
		// Gate check: turn limit
		if err := a.gate.CheckTurns(turn); err != nil {
			log.Printf("[agent] gate: %v", err)
			break
		}

		// Call LLM
		req := &llm.ChatRequest{
			Messages:    messages,
			Tools:       toolDefs,
			Temperature: a.cfg.Temperature,
			MaxTokens:   a.cfg.MaxTokens,
		}

		resp, err := a.llm.Complete(ctx, req)
		if err != nil {
			log.Printf("[agent] LLM error on turn %d: %v", turn, err)
			break
		}

		if len(resp.Choices) == 0 {
			log.Printf("[agent] LLM returned no choices on turn %d", turn)
			break
		}

		choice := resp.Choices[0]
		assistantMsg := choice.Message

		// Append assistant message to conversation
		messages = append(messages, assistantMsg)

		// Log any text content
		if assistantMsg.Content != "" {
			log.Printf("[agent] turn %d: %s", turn, Text.TruncateLog(assistantMsg.Content, 200))
		}

		// Check finish reason
		if choice.FinishReason == "stop" || choice.FinishReason == "length" {
			log.Printf("[agent] investigation complete (reason=%s, turns=%d)",
				choice.FinishReason, turn+1)
			break
		}

		// Process tool calls
		if len(assistantMsg.ToolCalls) == 0 {
			log.Printf("[agent] no tool calls and not stopped, ending")
			break
		}

		for _, tc := range assistantMsg.ToolCalls {
			result, execErr := a.executeToolCall(ctx, tc, trigger.AlertID, intent, trigger.Scope)

			toolMsg := llm.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    result,
			}
			if execErr != nil {
				toolMsg.Content = fmt.Sprintf("error: %v", execErr)
			}
			messages = append(messages, toolMsg)
		}

		log.Printf("[agent] turn %d: %d tool calls processed (tokens: %d)",
			turn, len(assistantMsg.ToolCalls), resp.Usage.TotalTokens)

		// Check for user interjection before next LLM call
		if a.interjections != nil {
			select {
			case msg := <-a.interjections:
				messages = append(messages, llm.Message{
					Role:    "user",
					Content: fmt.Sprintf("[Operator interjection]: %s", msg),
				})
				log.Printf("[agent] interjection injected: %s", Text.TruncateLog(msg, 100))
			default:
			}
		}
	}

	elapsed := time.Since(startTime)
	log.Printf("[agent] investigation finished in %s (alert=%s)", elapsed.Round(time.Millisecond), trigger.AlertID)
}

/*
 * RunReActSync runs the ReAct loop synchronously and returns a SyncResult.
 * desc: Same as investigateReAct but returns the verdict instead of fire-and-forget.
 *       Used by the API when mode=react. Goes through the same pipeline as DAG
 *       (IGX gate, scope checks, audit, tool execution) but with sequential
 *       reason-act-observe dispatch instead of parallel DAG execution.
 * param: ctx - context for the investigation.
 * param: trigger - the investigation trigger.
 * return: SyncResult with verdict, or error.
 */
func (a *Agent) RunReActSync(ctx context.Context, trigger Trigger) (*SyncResult, error) {
	log.Printf("[react] sync investigation: type=%s alert=%s source=%s",
		trigger.Type, trigger.AlertID, trigger.Source)

	a.investigating.Store(true)
	defer func() {
		a.investigating.Store(false)
		for {
			select {
			case <-a.interjections:
			default:
				return
			}
		}
	}()

	startTime := time.Now()

	// Get relevant tools and skills — same set the DAG planner sees
	relevant := a.relevantTools(ctx, formatTrigger(trigger), trigger.Scope)
	toolDefs := a.registry.ToolDefsForNames(relevant)

	// Build initial messages — no skill guidance injection (same as native planner).
	// Skills are available as tool descriptions in the API tools array.
	messages := BuildMessagesWithHistory(
		a.systemPrompt(),
		formatTrigger(trigger),
		trigger.History,
	)

	log.Printf("[react] sees %d tools (%d callable, %d guidance-only)", len(relevant), len(toolDefs), len(relevant)-len(toolDefs))

	if len(toolDefs) == 0 {
		return &SyncResult{Verdict: "No tools available."}, nil
	}

	// IGX: derive intent. Auto falls back to the registry's default rank.
	intent := trigger.Intent()
	if intent == gates.IntentAuto {
		intent = gates.Intent(a.intentRegistry.DefaultRank())
	}

	var verdict string
	totalToolCalls := 0
	totalLLMCalls := 0

	for turn := 0; turn < a.cfg.MaxTurns; turn++ {
		if err := a.gate.CheckTurns(turn); err != nil {
			break
		}

		// First turn: force tool use so ReAct doesn't skip tools entirely.
		// Subsequent turns: let the model decide (auto).
		toolChoice := "auto"
		if turn == 0 {
			toolChoice = "required"
		}

		resp, err := a.llm.Complete(ctx, &llm.ChatRequest{
			Messages:    messages,
			Tools:       toolDefs,
			ToolChoice:  toolChoice,
			Temperature: a.cfg.Temperature,
			MaxTokens:   a.cfg.MaxTokens,
		})
		if err != nil {
			return nil, fmt.Errorf("react LLM error turn %d: %w", turn, err)
		}
		totalLLMCalls++

		if len(resp.Choices) == 0 {
			break
		}

		choice := resp.Choices[0]
		assistantMsg := choice.Message
		messages = append(messages, assistantMsg)

		// Stream text content as verdict chunks
		if assistantMsg.Content != "" {
			a.broadcastDAGEvent(DAGEvent{Type: "verdict", Text: assistantMsg.Content})
			verdict = assistantMsg.Content
		}

		if choice.FinishReason == "stop" || choice.FinishReason == "length" {
			log.Printf("[react] complete (reason=%s, turns=%d, tools=%d, llm=%d)",
				choice.FinishReason, turn+1, totalToolCalls, totalLLMCalls)
			break
		}

		if len(assistantMsg.ToolCalls) == 0 {
			break
		}

		// Broadcast tool calls as DAG events so the frontend can show them
		for _, tc := range assistantMsg.ToolCalls {
			totalToolCalls++
			nodeID := fmt.Sprintf("react_%d", totalToolCalls)

			// Compact params for display
			paramsStr := tc.Function.Arguments
			if len(paramsStr) > 120 {
				paramsStr = paramsStr[:120] + "..."
			}

			a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: nodeID, Node: &NodeInfo{
				ID: nodeID, Type: "tool", State: "running",
				Tool: tc.Function.Name, Tag: tc.Function.Name,
				Params: paramsStr,
			}})

			toolStart := time.Now()
			result, execErr := a.executeToolCall(ctx, tc, trigger.AlertID, intent, trigger.Scope)
			toolMs := time.Since(toolStart).Milliseconds()

			toolMsg := llm.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    result,
			}
			if execErr != nil {
				toolMsg.Content = fmt.Sprintf("error: %v", execErr)
				a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: nodeID, Node: &NodeInfo{
					ID: nodeID, Type: "tool", State: "failed",
					Tool: tc.Function.Name, Tag: tc.Function.Name,
					Ms: toolMs, Error: execErr.Error(),
					Params: paramsStr,
				}})
			} else {
				truncResult := result
				if len(truncResult) > 200 {
					truncResult = truncResult[:200] + "..."
				}
				a.broadcastDAGEvent(DAGEvent{Type: "node", NodeID: nodeID, Node: &NodeInfo{
					ID: nodeID, Type: "tool", State: "resolved",
					Tool: tc.Function.Name, Tag: tc.Function.Name,
					Ms: toolMs, ResultSize: len(result),
					Result: truncResult, Params: paramsStr,
				}})
			}
			messages = append(messages, toolMsg)
		}

		log.Printf("[react] turn %d: %d tool calls (tokens: %d)",
			turn, len(assistantMsg.ToolCalls), resp.Usage.TotalTokens)

		// Interjection check
		if a.interjections != nil {
			select {
			case msg := <-a.interjections:
				messages = append(messages, llm.Message{
					Role:    "user",
					Content: fmt.Sprintf("[Operator interjection]: %s", msg),
				})
			default:
			}
		}
	}

	elapsed := time.Since(startTime)
	log.Printf("[react] sync complete in %s (alert=%s, tools=%d, llm=%d)",
		elapsed.Round(time.Millisecond), trigger.AlertID, totalToolCalls, totalLLMCalls)

	return &SyncResult{
		Verdict:  verdict,
		Nodes:    totalToolCalls,
		LLMCalls: totalLLMCalls,
	}, nil
}

/*
 * executeToolCall runs a single tool call through the IGX gate pipeline.
 * desc: Performs scope check, tool lookup, rate limit check, parameter parsing,
 *       IGX triad check, optional external clearance check, execution, audit,
 *       and result truncation.
 * param: ctx - context for execution.
 * param: tc - the LLM tool call to execute.
 * param: alertID - the investigation alert ID.
 * param: intent - the IGX intent level.
 * param: scope - resolved tool access scope (nil for full access).
 * return: result string and error.
 */
func (a *Agent) executeToolCall(ctx context.Context, tc llm.ToolCall,
	alertID string, intent gates.Intent, scope *ResolvedScope) (string, error) {

	toolName := tc.Function.Name

	// Scope check: reject tools not in scope (wildcard "*" allows all)
	if scope != nil && !scope.AllowedTools["*"] && !scope.AllowedTools[toolName] {
		return "", fmt.Errorf("gate: %s not in user scope", toolName)
	}

	// Look up tool
	skill, ok := a.registry.Get(toolName)
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}

	// Gate: rate limit
	if err := a.gate.CheckRateLimit(); err != nil {
		a.gate.Audit(gates.AuditEntry{
			Tool:    toolName,
			AlertID: alertID,
			Error:   err.Error(),
		})
		return "", err
	}

	// Parse parameters
	var params map[string]any
	if tc.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
			return "", fmt.Errorf("invalid parameters: %w", err)
		}
	}

	// Resolve the tool's effective impact via the intent registry.
	impact := a.intentRegistry.ResolveToolIntent(toolName, skill, params)

	// Gate: IGX triad check with scope — impact <= min(intent, clearance, scope_cap)
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

	// Clearance: check external authorization endpoint
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

	// Execute
	result, err := skill.Execute(ctx, params)

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

	if err != nil {
		return "", err
	}

	// Truncate large results
	if len(result) > maxToolResultLen {
		result = result[:maxToolResultLen] + "\n... (truncated)"
	}

	return result, nil
}

/*
 * formatTrigger converts a trigger into a human-readable message for the LLM.
 * desc: For chat queries, extracts and returns the user's question directly.
 *       For all other trigger types, formats as an alert with type, ID, source,
 *       and data fields.
 * param: t - the Trigger to format.
 * return: formatted trigger string for LLM consumption.
 */
func formatTrigger(t Trigger) string {
	// Chat queries: present the user's question directly, not wrapped in alert format.
	// The planner should see "what processes are running?" not "Alert ID: chat-173..."
	if t.Type == "chat_query" {
		var data map[string]string
		if json.Unmarshal(t.Data, &data) == nil {
			if q := data["query"]; q != "" {
				return q
			}
		}
		return string(t.Data)
	}

	var sb strings.Builder
	sb.WriteString("## Alert\n\n")
	sb.WriteString(fmt.Sprintf("**Type:** %s\n", t.Type))
	if t.AlertID != "" {
		sb.WriteString(fmt.Sprintf("**Alert ID:** %s\n", t.AlertID))
	}
	if t.Source != "" {
		sb.WriteString(fmt.Sprintf("**Source:** %s\n", t.Source))
	}
	if len(t.Data) > 0 {
		sb.WriteString(fmt.Sprintf("\n**Alert Data:**\n```json\n%s\n```\n", string(t.Data)))
	}
	sb.WriteString("\nInvestigate this alert. Use your available tools to gather more context, then determine the appropriate response.")
	return sb.String()
}
