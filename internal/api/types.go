package api

// ExecuteRequest is the payload for POST /api/v1/execute.
type ExecuteRequest struct {
	Query         string `json:"query"`
	Mode          string `json:"mode,omitempty"`           // "reflect", "nReflect", "orchestrator"
	Intent        string `json:"intent,omitempty"`         // any intent name registered in the intent registry (loaded from config/DB)
	SessionID     string `json:"session_id,omitempty"`     // conversation session for memory
	AggMode       *int   `json:"agg_mode,omitempty"`       // 0=skip, 1=executor model (default), 2=reasoning model
	ExecutionMode string `json:"execution_mode,omitempty"` // "interactive" or "autonomous" (per-request override)
	// Per-request model routing (optional; empty ⇒ configured default). The
	// host selects a provider name (as configured in kaiju's providers block)
	// and a model id for each lane — heavy (answer/reasoning) and executor
	// (light: classify/route/reflect). Requests carry only the selection; the
	// provider keys live in kaiju's config, never in the request.
	Provider         string `json:"provider,omitempty"`          // heavy-lane provider: openai|anthropic|openrouter|selfhosted
	Model            string `json:"model,omitempty"`             // heavy-lane model id
	ExecutorProvider string `json:"executor_provider,omitempty"` // light-lane provider
	ExecutorModel    string `json:"executor_model,omitempty"`    // light-lane model id
	// Vision lane: the model that answers questions about attached images
	// directly (no planner/tools). Empty ⇒ the configured default vision model.
	VisionProvider string `json:"vision_provider,omitempty"`
	VisionModel    string `json:"vision_model,omitempty"`
	// Chat lane: when ChatMode is true, the turn is answered by a direct
	// completion (no planner/DAG/tools) — for plain conversation and non-tool
	// models. The model is the override below, else the configured chat default,
	// else the reasoning model.
	ChatMode     bool   `json:"chat_mode,omitempty"`
	ChatProvider string `json:"chat_provider,omitempty"`
	ChatModel    string `json:"chat_model,omitempty"`
	// Regenerate re-runs the last turn: the previous assistant reply is dropped
	// and the last user message is answered again. Query is ignored (taken from
	// history). Session-scoped and ownership-checked.
	Regenerate bool `json:"regenerate,omitempty"`
}

// ActionInfo describes a recommended follow-up action from the aggregator.
type ActionInfo struct {
	Tool   string         `json:"tool"`
	Params map[string]any `json:"params"`
}

// ExecuteResponse is returned from POST /api/v1/execute.
type ExecuteResponse struct {
	Verdict    string       `json:"verdict"`
	Actions    []ActionInfo `json:"actions,omitempty"`    // recommended follow-up actions (caller decides)
	Gaps       []string     `json:"gaps,omitempty"`       // capability gaps (missing tools)
	DAGID      string       `json:"dag_id,omitempty"`
	Nodes      int          `json:"nodes"`
	LLMCalls   int          `json:"llm_calls"`
	Tokens     int64        `json:"tokens"` // total LLM tokens for THIS request (non-streamed calls; see llm.CompleteStream)
	DurationMs int64        `json:"duration_ms"`
	Error      string       `json:"error,omitempty"`
}

// ToolInfo describes a registered tool.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Impact      int    `json:"default_impact"`
	Enabled     bool   `json:"enabled"`
	Source      string `json:"source"` // "builtin", "custom", "skillmd:path"
}

// StatusResponse is returned from GET /api/v1/status.
type StatusResponse struct {
	Status      string `json:"status"` // "idle", "investigating"
	DAGMode     string `json:"dag_mode"`
	SafetyLevel int    `json:"safety_level"`
	ToolCount   int    `json:"tool_count"`
}
