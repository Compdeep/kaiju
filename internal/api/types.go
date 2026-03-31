package api

// ExecuteRequest is the payload for POST /api/v1/execute.
type ExecuteRequest struct {
	Query      string `json:"query"`
	Mode       string `json:"mode,omitempty"`        // "reflect", "nReflect", "orchestrator"
	Intent     string `json:"intent,omitempty"`      // "observe", "operate", "override"
	SessionID  string `json:"session_id,omitempty"`  // conversation session for memory
	AggMode    *int   `json:"agg_mode,omitempty"`    // 0=skip, 1=executor model (default), 2=reasoning model
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
