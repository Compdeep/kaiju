package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/user/kaiju/internal/agent"
	"github.com/user/kaiju/internal/agent/gates"
	"github.com/user/kaiju/internal/agent/llm"
	"github.com/user/kaiju/internal/clearance"
	"github.com/user/kaiju/internal/db"
	"github.com/user/kaiju/internal/gateway"
	"github.com/user/kaiju/internal/memory"
)

/*
 * API provides the REST execution API.
 * desc: Central API handler set that exposes DAG execution, tool listing, status, sessions, memories, and clearance endpoints.
 */
type API struct {
	agent            *agent.Agent
	safetyLevel      int
	db               *db.DB
	llmClient        *llm.Client
	clearanceChecker *clearance.Checker
}

/*
 * New creates an API handler set.
 * desc: Constructs an API instance wired to the agent, database, LLM client, and clearance checker.
 * param: ag - the agent that executes DAG queries
 * param: safetyLevel - default safety/intent level for unauthenticated requests
 * param: database - database handle for persistence
 * param: llmClient - LLM client for memory operations
 * param: cc - clearance checker for external approval endpoints
 * return: a configured API instance
 */
func New(ag *agent.Agent, safetyLevel int, database *db.DB, llmClient *llm.Client, cc *clearance.Checker) *API {
	return &API{agent: ag, safetyLevel: safetyLevel, db: database, llmClient: llmClient, clearanceChecker: cc}
}

/*
 * RegisterRoutes mounts API routes on the given mux.
 * desc: Registers all REST endpoints for execution, sessions, memories, interjection, and clearance.
 * param: mux - the HTTP serve mux to attach routes to
 */
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/execute", a.handleExecute)
	mux.HandleFunc("GET /api/v1/tools", a.handleListTools)
	mux.HandleFunc("GET /api/v1/status", a.handleStatus)
	// Sessions
	mux.HandleFunc("POST /api/v1/sessions", a.handleCreateSession)
	mux.HandleFunc("GET /api/v1/sessions", a.handleListSessions)
	mux.HandleFunc("DELETE /api/v1/sessions/{id}", a.handleDeleteSession)
	mux.HandleFunc("GET /api/v1/sessions/{id}/messages", a.handleGetMessages)
	mux.HandleFunc("POST /api/v1/sessions/{id}/compact", a.handleCompactSession)
	mux.HandleFunc("POST /api/v1/sessions/{id}/trace", a.handleSaveTrace)
	// Memories
	mux.HandleFunc("POST /api/v1/memories", a.handleStoreMemory)
	mux.HandleFunc("GET /api/v1/memories", a.handleSearchMemories)
	mux.HandleFunc("DELETE /api/v1/memories/{id}", a.handleDeleteMemory)
	// Interjection
	mux.HandleFunc("POST /api/v1/interject", a.handleInterject)
	// Clearance endpoints
	mux.HandleFunc("GET /api/v1/clearance", a.handleListClearanceEndpoints)
	mux.HandleFunc("POST /api/v1/clearance", a.handleUpsertClearanceEndpoint)
	mux.HandleFunc("DELETE /api/v1/clearance/{tool}", a.handleDeleteClearanceEndpoint)
}

/*
 * handleExecute processes a DAG execution request.
 * desc: Decodes the execute payload, resolves user context and intent from JWT, loads memory, runs the DAG, and returns the result.
 * param: w - HTTP response writer
 * param: r - HTTP request containing an ExecuteRequest JSON body
 */
func (a *API) handleExecute(w http.ResponseWriter, r *http.Request) {
	var req ExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Query == "" {
		jsonError(w, "query is required", http.StatusBadRequest)
		return
	}

	// Map intent string to gates.Intent
	intent := a.safetyLevel
	switch req.Intent {
	case "observe", "tell":
		intent = int(gates.IntentObserve)
	case "operate", "triage":
		intent = int(gates.IntentOperate)
	case "override", "act":
		intent = int(gates.IntentOverride)
	}

	// Resolve user context from JWT claims
	var userID string
	var scope *agent.ResolvedScope
	if claims, ok := gateway.ClaimsFromContext(r.Context()); ok {
		userID = claims.Username
		if claims.MaxIntent < intent {
			intent = claims.MaxIntent
		}

		// Resolve user's tool scope from DB
		if a.db != nil {
			if user, err := a.db.GetUser(userID); err == nil {
				if dbScope, err := a.db.ResolveUserScope(user); err == nil {
					scope = &agent.ResolvedScope{
						Username:     dbScope.Username,
						AllowedTools: dbScope.AllowedTools,
						MaxImpact:    dbScope.MaxImpact,
						MaxIntent:    dbScope.MaxIntent,
					}
					// Cap intent by scope
					if dbScope.MaxIntent < intent {
						intent = dbScope.MaxIntent
					}
				}
			}
		}
	}

	trigger := agent.Trigger{
		Type:    "chat_query",
		AlertID: fmt.Sprintf("api-%d", time.Now().UnixNano()),
		Data:    mustMarshal(map[string]string{"query": req.Query}),
		Source:  "api",
		Scope:   scope,
	}

	// Memory integration: load conversation history + long-term context
	var memMgr *memory.Manager
	if req.SessionID != "" && userID != "" && a.db != nil {
		memMgr = memory.New(a.db, a.llmClient, userID)

		// Load conversation history
		if history, err := memMgr.LoadHistory(r.Context(), req.SessionID, 50); err == nil {
			trigger.SessionID = req.SessionID
			trigger.History = history
		}

		// Inject long-term memory context
		if ltCtx, err := memMgr.InjectLongTermContext(r.Context()); err == nil && ltCtx != "" {
			trigger.History = append(
				[]llm.Message{{Role: "system", Content: ltCtx}},
				trigger.History...,
			)
		}

		// Store the user message
		memMgr.StoreMessage(req.SessionID, "user", req.Query)
	}
	if req.Mode != "" {
		trigger.DAGMode = req.Mode
	}
	if req.AggMode != nil {
		trigger.AggMode = *req.AggMode
	} else {
		trigger.AggMode = -1 // default: auto (reflector decides)
	}
	maxIntent := intent
	trigger.MaxIntent = &maxIntent

	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	result, err := a.agent.RunDAGSync(ctx, trigger)
	elapsed := time.Since(start)

	if err != nil {
		log.Printf("[api] execute error: %v", err)
		jsonResponse(w, ExecuteResponse{
			Error:      err.Error(),
			DurationMs: elapsed.Milliseconds(),
		}, http.StatusInternalServerError)
		return
	}

	// Store assistant response and auto-compact
	if memMgr != nil && result.Verdict != "" {
		memMgr.StoreMessage(req.SessionID, "assistant", result.Verdict)
		if shouldCompact, _ := memMgr.ShouldCompact(req.SessionID); shouldCompact {
			go memMgr.Compact(context.Background(), req.SessionID)
		}
	}

	// Convert actions to API type
	var apiActions []ActionInfo
	for _, a := range result.Actions {
		apiActions = append(apiActions, ActionInfo{Tool: a.Tool, Params: a.Params})
	}

	jsonResponse(w, ExecuteResponse{
		Verdict:    result.Verdict,
		Actions:    apiActions,
		Gaps:       result.Gaps,
		DAGID:      trigger.AlertID,
		Nodes:      result.Nodes,
		LLMCalls:   result.LLMCalls,
		DurationMs: elapsed.Milliseconds(),
	}, http.StatusOK)
}

/*
 * handleInterject sends a human interjection message to the active investigation.
 * desc: Accepts a message and injects it into the running DAG if one is active.
 * param: w - HTTP response writer
 * param: r - HTTP request containing a JSON body with a "message" field
 */
func (a *API) handleInterject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		jsonError(w, "message is required", http.StatusBadRequest)
		return
	}

	ok := a.agent.Interject(req.Message)
	if !ok {
		jsonResponse(w, map[string]any{"sent": false, "reason": "no active investigation"}, http.StatusOK)
		return
	}

	log.Printf("[api] interjection sent: %s", req.Message)
	jsonResponse(w, map[string]any{"sent": true}, http.StatusOK)
}

/*
 * handleListTools returns the list of all registered tools and their metadata.
 * desc: Queries the agent registry and returns tool name, description, impact, enabled state, and source.
 * param: w - HTTP response writer
 */
func (a *API) handleListTools(w http.ResponseWriter, _ *http.Request) {
	regInfos := a.agent.Registry().ListInfo()
	toolInfos := make([]ToolInfo, 0, len(regInfos))
	for _, ri := range regInfos {
		toolInfos = append(toolInfos, ToolInfo{
			Name:        ri.Name,
			Description: ri.Description,
			Impact:      ri.Impact,
			Enabled:     ri.Enabled,
			Source:      ri.Source,
		})
	}
	jsonResponse(w, toolInfos, http.StatusOK)
}

/*
 * handleStatus returns the current agent status.
 * desc: Reports idle/investigating state, DAG mode, safety level, and tool count.
 * param: w - HTTP response writer
 */
func (a *API) handleStatus(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, StatusResponse{
		Status:      "idle",
		DAGMode:     "orchestrator",
		SafetyLevel: a.safetyLevel,
		ToolCount:   len(a.agent.Registry().List()),
	}, http.StatusOK)
}

/*
 * jsonResponse writes a JSON-encoded response with the given status code.
 * desc: Serializes data as JSON and writes it to the response with Content-Type set.
 * param: w - HTTP response writer
 * param: data - the value to encode as JSON
 * param: status - HTTP status code
 */
func jsonResponse(w http.ResponseWriter, data any, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

/*
 * jsonError writes a JSON error response.
 * desc: Convenience wrapper that sends {"error": msg} with the given status code.
 * param: w - HTTP response writer
 * param: msg - error message string
 * param: status - HTTP status code
 */
func jsonError(w http.ResponseWriter, msg string, status int) {
	jsonResponse(w, map[string]string{"error": msg}, status)
}

/*
 * mustMarshal marshals a value to JSON, panicking on failure.
 * desc: Used for values that are guaranteed to be marshalable (e.g. map[string]string).
 * param: v - the value to marshal
 * return: the JSON-encoded bytes as a json.RawMessage
 */
func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
