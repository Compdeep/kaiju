package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/internal/agent"
	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/agent/prompt"
	"github.com/Compdeep/kaiju/internal/agent/uploads"
	"github.com/Compdeep/kaiju/internal/clearance"
	"github.com/Compdeep/kaiju/internal/db"
	"github.com/Compdeep/kaiju/internal/gateway"
	"github.com/Compdeep/kaiju/internal/memory"
	"github.com/Compdeep/kaiju/internal/tokens"
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
	uploadProc       *uploads.Processor // nil until SetUploadProcessor is called from main.go
}

// resolveVision picks the vision model for a turn: the per-request override
// (host selection) wins, else the agent's configured default (which the config
// API live-updates). Empty model ⇒ no dedicated vision lane.
func (a *API) resolveVision(req ExecuteRequest) (provider, model string) {
	if req.VisionModel != "" {
		return req.VisionProvider, req.VisionModel
	}
	return a.agent.VisionModel()
}

// resolveChat picks the chat-lane model: per-request override → configured chat
// default → ("","") which OneShot resolves to the reasoning model.
func (a *API) resolveChat(req ExecuteRequest) (provider, model string) {
	if req.ChatModel != "" {
		return req.ChatProvider, req.ChatModel
	}
	return a.agent.ChatModel()
}

// Lane personas now live in the composable prompt package: the chat lane uses
// SOUL + prompt.Chat (assembled in Agent.Converse); the vision fallback uses
// SOUL + prompt.Vision. Both are operator-overridable via dataDir/prompts.md.

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
	mux.HandleFunc("POST /api/v1/oneshot", a.handleOneShot)
	mux.HandleFunc("GET /api/v1/tools", a.handleListTools)
	mux.HandleFunc("GET /api/v1/status", a.handleStatus)
	mux.HandleFunc("GET /api/v1/usage", a.handleUsage)
	mux.HandleFunc("GET /api/v1/workspace/files", a.handleWorkspaceFiles)
	mux.HandleFunc("GET /api/v1/workspace/serve", a.handleWorkspaceServe)
	mux.HandleFunc("POST /api/v1/workspace/write", a.handleWorkspaceWrite)
	mux.HandleFunc("GET /api/v1/workspace/live/", a.handleCanvasServe)
	// Sessions
	mux.HandleFunc("POST /api/v1/sessions", a.handleCreateSession)
	mux.HandleFunc("GET /api/v1/sessions", a.handleListSessions)
	mux.HandleFunc("DELETE /api/v1/sessions/{id}", a.handleDeleteSession)
	mux.HandleFunc("GET /api/v1/sessions/{id}/messages", a.handleGetMessages)
	mux.HandleFunc("PATCH /api/v1/sessions/{id}/messages/{msgId}", a.handleEditMessage)
	mux.HandleFunc("DELETE /api/v1/sessions/{id}/messages/{msgId}", a.handleDeleteMessage)
	mux.HandleFunc("POST /api/v1/sessions/{id}/compact", a.handleCompactSession)
	mux.HandleFunc("POST /api/v1/sessions/{id}/trace", a.handleSaveTrace)
	// Uploads — per-session attachments
	mux.HandleFunc("POST /api/v1/sessions/{id}/uploads", a.handleUploadFile)
	mux.HandleFunc("GET /api/v1/sessions/{id}/uploads", a.handleListUploads)
	mux.HandleFunc("DELETE /api/v1/sessions/{id}/uploads/{name}", a.handleDeleteUpload)
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

// execResult builds the common success response shared by every lane (chat,
// vision, executive): the verdict, run counts, token totals (in/out split read
// from the ctx accumulator), and elapsed time. Callers that carry extra fields
// (the executive's Actions/Gaps) set them on the returned value before sending.
// Single source so the response shape can't drift across lanes.
func execResult(ctx context.Context, alertID, verdict string, nodes, llmCalls int, total int64, elapsed time.Duration) ExecuteResponse {
	return ExecuteResponse{
		Verdict:    verdict,
		DAGID:      alertID,
		Nodes:      nodes,
		LLMCalls:   llmCalls,
		Tokens:     total,
		TokensIn:   tokens.RunIn(ctx),
		TokensOut:  tokens.RunOut(ctx),
		DurationMs: elapsed.Milliseconds(),
	}
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
	if req.Query == "" && !req.Regenerate {
		jsonError(w, "query is required", http.StatusBadRequest)
		return
	}

	// Resolve intent via the registry. Everything is ranks — no name
	// knowledge in Go, no legacy alias fallbacks. Unknown names fall
	// through to the configured safety level.
	intent := a.safetyLevel
	if req.Intent != "" {
		if i, ok := a.agent.Intents().ByName(req.Intent); ok {
			intent = i.Rank
		}
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

	// Regenerate: re-run the last turn. Drop the previous assistant reply and
	// answer the last user message again. Ownership-checked; the delete is scoped
	// to the session, so a user can only regenerate their own chat. The query is
	// taken from history (not the request), and the user message is NOT re-stored.
	regenerating := false
	if req.Regenerate {
		if req.SessionID == "" || userID == "" || a.db == nil {
			jsonError(w, "regenerate requires a session", http.StatusBadRequest)
			return
		}
		if _, err := a.db.GetSessionForUser(req.SessionID, userID); err != nil {
			jsonError(w, "session not found", http.StatusNotFound)
			return
		}
		msgs, _ := a.db.GetRecentMessages(req.SessionID, 200)
		lastUser := -1
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "user" {
				lastUser = i
				break
			}
		}
		if lastUser < 0 {
			jsonError(w, "nothing to regenerate", http.StatusBadRequest)
			return
		}
		if lastUser+1 < len(msgs) { // drop the assistant reply after it
			a.db.DeleteMessagesFrom(req.SessionID, msgs[lastUser+1].ID)
		}
		req.Query = msgs[lastUser].Content
		regenerating = true
	}

	trigger := agent.Trigger{
		Type:    "chat_query",
		AlertID: fmt.Sprintf("api-%d", time.Now().UnixNano()),
		Data:    mustMarshal(map[string]string{"query": req.Query}),
		Source:  "api",
		Scope:   scope,
		// Per-request model routing (host selection; keys stay in kaiju config).
		Provider:         req.Provider,
		Model:            req.Model,
		ExecutorProvider: req.ExecutorProvider,
		ExecutorModel:    req.ExecutorModel,
	}

	// ── Memory boundary (chat input) ──────────────────────────────────────
	//
	// THIS IS THE ONLY PLACE memory enters the agent's reasoning context.
	// Memory loading happens HERE at the chat input boundary, NOT inside
	// the execution layer (ContextGate, sources, graph nodes). The execution
	// layer must remain unaware of memory — see the architectural doc at the
	// top of internal/agent/contextgate.go for the rationale.
	//
	// Why this boundary matters (anti-prompt-injection security):
	//   The agent runs untrusted tool output (bash results, web fetches,
	//   compute LLM responses, etc.) through reflectors and debuggers. If
	//   any of those code paths could query or write memory, a malicious
	//   tool output could exfiltrate or rewrite a user's stored facts.
	//   Keeping memory out of the execution layer means the only memory
	//   reads are at the chat input (here, attested by the authenticated
	//   user) and the only memory writes are at the chat output (the
	//   aggregator's verdict, also a single attested step).
	//
	// If you need memory inside a node, you are doing something wrong.
	// Reach out to the architecture before adding any memory access path.
	var memMgr *memory.Manager
	if req.SessionID != "" && userID != "" && a.db != nil {
		memMgr = memory.New(a.db, a.llmClient, userID)
		trigger.SessionID = req.SessionID

		// Planner-only memory context. The chat lane is a plain conversational
		// turn — it builds its OWN clean, verbatim history below and must not
		// receive the planner's truncated history or cross-session long-term
		// memory. So skip both entirely in chat mode (also saves two DB searches
		// per chat turn). Only the message WRITE below is shared.
		if !req.ChatMode {
			// Load conversation history
			if history, err := memMgr.LoadHistory(r.Context(), req.SessionID, 50); err == nil {
				trigger.History = history
			}

			// Inject long-term memory context as a system message at the head
			// of the history slice. From here on, memory is opaque conversation
			// data — execution-layer code may READ trigger.History but never
			// distinguishes "real history" from "injected memory."
			if ltCtx, err := memMgr.InjectLongTermContext(r.Context()); err == nil && ltCtx != "" {
				trigger.History = append(
					[]llm.Message{{Role: "system", Content: ltCtx}},
					trigger.History...,
				)
			}
		}

		// Store the user message immediately (chat output boundary for the
		// user-side turn). The assistant turn is stored after the aggregator
		// runs, also outside the execution layer. On regenerate the user message
		// is already in history, so don't duplicate it.
		if !regenerating {
			memMgr.StoreMessage(req.SessionID, "user", req.Query)
		}
	}
	if req.Mode != "" {
		trigger.DAGMode = req.Mode
	}
	if req.ExecutionMode != "" {
		trigger.ExecutionMode = req.ExecutionMode
	}
	if req.AggMode != nil {
		trigger.AggMode = *req.AggMode
	} else {
		trigger.AggMode = -1 // default: auto (reflector decides)
	}
	// Only pin the intent rank when the client explicitly asked for one.
	// When absent, leave MaxIntent nil so the planner infers from tool
	// impacts (IntentAuto). The scheduler caps the inferred value by
	// clearance and the user's scope ceiling.
	if req.Intent != "" {
		maxIntent := intent
		trigger.MaxIntent = &maxIntent
	}

	start := time.Now()
	// No HTTP-level timeout — the DAG's own wall clock (budget.WallClock) is
	// the authority. A separate HTTP timeout caused connection resets and
	// ghost retries when the DAG outlived the HTTP deadline.
	ctx := r.Context()
	// Attribute this run to the calling principal (JWT sub) and open a per-run
	// token counter; both ride the ctx through SubmitSync into every LLM call on
	// the sync path, so RunTotal(ctx) below is this request's exact token cost —
	// the value the host (makeen) persists per user for durable billing.
	ctx = tokens.WithRun(tokens.WithPrincipal(ctx, userID))

	// Vision routing. When the session has image uploads:
	//   • if a vision model is configured/selected, the image question is
	//     answered DIRECTLY by that model (no planner/tools) — so a tool-less
	//     vision model like Qwen-VL works and images always reach a capable model.
	//   • otherwise images ride the ctx and the agent attaches them to heavy-lane
	//     calls only if the reasoning model itself supports vision.
	var visionImgs []string
	if a.uploadProc != nil && req.SessionID != "" {
		visionImgs = a.uploadProc.SessionImageDataURIs(req.SessionID)
	}

	// Chat lane. When chat mode is on, answer with a direct completion — no
	// planner/DAG/tools — so plain conversation and non-tool models (roleplay
	// fine-tunes) work. Takes precedence over the vision lane; if the session has
	// images and the chat model is vision-capable, they ride along.
	if req.ChatMode {
		cp, cm := a.resolveChat(req)
		dp, dmodel := a.agent.ChatModel()
		log.Printf("[chat] chat_mode=true → direct to provider=%q model=%q (req.chat_model=%q, agent default=%q/%q, tools=%v)", cp, cm, req.ChatModel, dp, dmodel, req.ChatTools)
		// History: recent, verbatim, no cross-session long-term memory. The user's
		// current message was already stored above, so it's the last turn here.
		var history []llm.Message
		if memMgr != nil {
			history, _ = memMgr.LoadChatHistory(ctx, req.SessionID, 40)
		}
		// The chat lane's tool permission IS the tool list (API-driven). A request
		// may name its own chat_tools; if it sends none, fall back to the instance
		// default (chat.tools config). Empty ⇒ pure chat. The list does NOT inherit
		// the JWT's broader scope — a chat turn can only run the tools named here.
		chatTools := req.ChatTools
		if len(chatTools) == 0 {
			chatTools = a.agent.ChatTools()
		}
		// When the agent is enabled for this chat, the tuned classifier decides —
		// reliably — whether this turn needs the agent, rather than leaving it to
		// the chat model's tool-choice (which under-delegates). investigate → run
		// the agent (its steps broadcast as DAG events for live progress), with the
		// conversation history for context; chat/meta → answer on the chat lane
		// below, with the agent removed from the tool list. Light tools (web_fetch)
		// stay model-driven in the chat lane — only the agent decision is gated here.
		agentEnabled := false
		for _, n := range chatTools {
			if n == "agent" {
				agentEnabled = true
				break
			}
		}
		if agentEnabled && a.agent.RouteChat(ctx, trigger.AlertID, req.Query) == "investigate" {
			log.Printf("[chat] classifier=investigate → running agent")
			verdict, nodes, llmCalls, aerr := a.agent.RunAgentTask(ctx, trigger.AlertID, req.Query, history)
			elapsed := time.Since(start)
			if aerr != nil {
				log.Printf("[api] chat→agent error: %v", aerr)
				jsonResponse(w, ExecuteResponse{Error: aerr.Error(), DurationMs: elapsed.Milliseconds()}, http.StatusInternalServerError)
				return
			}
			if memMgr != nil && verdict != "" {
				memMgr.StoreMessage(req.SessionID, "assistant", verdict)
				if shouldCompact, _ := memMgr.ShouldCompact(req.SessionID); shouldCompact {
					go memMgr.Compact(context.Background(), req.SessionID)
				}
			}
			jsonResponse(w, execResult(ctx, trigger.AlertID, verdict, nodes, llmCalls, tokens.RunTotal(ctx), elapsed), http.StatusOK)
			return
		}
		if agentEnabled {
			// classifier said chat/meta — don't offer the agent tool to the model.
			kept := chatTools[:0]
			for _, n := range chatTools {
				if n != "agent" {
					kept = append(kept, n)
				}
			}
			chatTools = kept
		}
		var chatScope *agent.ResolvedScope
		if len(chatTools) > 0 {
			chatScope = &agent.ResolvedScope{AllowedTools: map[string]bool{}}
			for _, name := range chatTools {
				chatScope.AllowedTools[name] = true
			}
		}
		res, cerr := a.agent.Converse(ctx, agent.ChatTurn{
			Provider:  cp,
			Model:     cm,
			History:   history,
			Query:     req.Query,
			ToolNames: chatTools,
			Images:    visionImgs,
			Scope:     chatScope,
			AlertID:   trigger.AlertID,
		})
		elapsed := time.Since(start)
		if cerr != nil {
			log.Printf("[api] chat execute error: %v", cerr)
			jsonResponse(w, ExecuteResponse{Error: cerr.Error(), DurationMs: elapsed.Milliseconds()}, http.StatusInternalServerError)
			return
		}
		if memMgr != nil && res.Content != "" {
			memMgr.StoreMessage(req.SessionID, "assistant", res.Content)
			// Keep the chat lane's memory bounded: summarise old turns into a
			// [Conversation summary] system message (which LoadChatHistory then
			// carries), so long threads aren't truncated to a goldfish window.
			if shouldCompact, _ := memMgr.ShouldCompact(req.SessionID); shouldCompact {
				go memMgr.Compact(context.Background(), req.SessionID)
			}
		}
		jsonResponse(w, execResult(ctx, trigger.AlertID, res.Content, 0, res.LLMCalls, int64(res.Tokens), elapsed), http.StatusOK)
		return
	}

	if len(visionImgs) > 0 {
		if vp, vm := a.resolveVision(req); vm != "" {
			visionSystem := agent.ComposeSystemPrompt(a.agent.SoulPrompt(), prompt.Vision)
			msgs := agent.BuildMessagesWithHistory(visionSystem, req.Query, trigger.History)
			llm.AttachImages(msgs, visionImgs)
			content, toks, verr := a.agent.OneShot(ctx, vp, vm, msgs, 0.3, 1024)
			elapsed := time.Since(start)
			if verr != nil {
				log.Printf("[api] vision execute error: %v", verr)
				jsonResponse(w, ExecuteResponse{Error: verr.Error(), DurationMs: elapsed.Milliseconds()}, http.StatusInternalServerError)
				return
			}
			if memMgr != nil && content != "" {
				memMgr.StoreMessage(req.SessionID, "assistant", content)
			}
			jsonResponse(w, execResult(ctx, trigger.AlertID, content, 0, 1, int64(toks), elapsed), http.StatusOK)
			return
		}
		ctx = agent.WithVisionImages(ctx, visionImgs)
	}

	result, err := a.agent.Kernel().SubmitSync(ctx, trigger)
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

	resp := execResult(ctx, trigger.AlertID, result.Verdict, result.Nodes, result.LLMCalls, tokens.RunTotal(ctx), elapsed)
	resp.Actions = apiActions
	resp.Gaps = result.Gaps
	jsonResponse(w, resp, http.StatusOK)
}

/*
 * handleInterject sends a human interjection message to the active investigation.
 * desc: Accepts a message and injects it into the running DAG if one is active.
 * param: w - HTTP response writer
 * param: r - HTTP request containing a JSON body with a "message" field
 */
func (a *API) handleInterject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
		Message   string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		jsonError(w, "message is required", http.StatusBadRequest)
		return
	}

	ok := a.agent.Interject(req.SessionID, req.Message)
	if !ok {
		jsonResponse(w, map[string]any{"sent": false, "reason": "no active investigation"}, http.StatusOK)
		return
	}

	log.Printf("[api] interjection sent: %s", req.Message)
	jsonResponse(w, map[string]any{"sent": true}, http.StatusOK)
}

/*
 * handleUsage returns in-memory LLM token-usage tallies since process start,
 * broken down per (principal, category). In-memory only — resets on restart.
 * Streamed calls (aggregator/verdict) are not yet counted (see llm.CompleteStream).
 * param: w - HTTP response writer
 */
func (a *API) handleUsage(w http.ResponseWriter, _ *http.Request) {
	usage, total := tokens.Snapshot()
	jsonResponse(w, map[string]any{"usage": usage, "total": total}, http.StatusOK)
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
 * handleWorkspaceFiles lists files in the workspace directory.
 * desc: Returns directory listing for the workspace, supporting relative path navigation.
 * param: w - HTTP response writer
 * param: r - HTTP request with optional ?path= query param
 */
func (a *API) handleWorkspaceFiles(w http.ResponseWriter, r *http.Request) {
	workspace := a.agent.Workspace()
	if workspace == "" {
		jsonError(w, "workspace not configured", http.StatusBadRequest)
		return
	}

	relPath := r.URL.Query().Get("path")
	fullPath := filepath.Join(workspace, relPath)

	// Security: ensure we don't escape workspace
	if !strings.HasPrefix(filepath.Clean(fullPath), filepath.Clean(workspace)) {
		jsonError(w, "path outside workspace", http.StatusForbidden)
		return
	}

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}

	type fileEntry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"is_dir"`
		Size  int64  `json:"size,omitempty"`
	}

	result := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		fe := fileEntry{Name: e.Name(), IsDir: e.IsDir()}
		if !e.IsDir() {
			if info, err := e.Info(); err == nil {
				fe.Size = info.Size()
			}
		}
		result = append(result, fe)
	}

	jsonResponse(w, map[string]any{
		"path":    relPath,
		"entries": result,
	}, http.StatusOK)
}

/*
 * handleWorkspaceServe serves a file from the workspace directory.
 * desc: Streams file content with appropriate MIME type for viewing/downloading.
 * param: w - HTTP response writer
 * param: r - HTTP request with ?path= query param
 */
func (a *API) handleWorkspaceServe(w http.ResponseWriter, r *http.Request) {
	workspace := a.agent.Workspace()
	if workspace == "" {
		jsonError(w, "workspace not configured", http.StatusBadRequest)
		return
	}

	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		jsonError(w, "path required", http.StatusBadRequest)
		return
	}

	fullPath := filepath.Join(workspace, relPath)
	if !strings.HasPrefix(filepath.Clean(fullPath), filepath.Clean(workspace)) {
		jsonError(w, "path outside workspace", http.StatusForbidden)
		return
	}

	http.ServeFile(w, r, fullPath)
}

/*
 * handleCanvasServe serves files from workspace/canvas/ with path-based URLs.
 * desc: Enables multi-file webapps in the canvas — relative imports (./style.css,
 *       ./app.js) resolve correctly because the URL path mirrors the filesystem.
 *       Path: /api/v1/workspace/canvas/{filepath...}
 */
func (a *API) handleCanvasServe(w http.ResponseWriter, r *http.Request) {
	workspace := a.agent.Workspace()
	if workspace == "" {
		jsonError(w, "workspace not configured", http.StatusBadRequest)
		return
	}

	// Strip prefix to get the relative path within project/
	relPath := strings.TrimPrefix(r.URL.Path, "/api/v1/workspace/live/")
	if relPath == "" {
		jsonError(w, "path required", http.StatusBadRequest)
		return
	}

	fullPath := filepath.Join(workspace, "project", relPath)
	if !strings.HasPrefix(filepath.Clean(fullPath), filepath.Clean(filepath.Join(workspace, "project"))) {
		jsonError(w, "path outside project directory", http.StatusForbidden)
		return
	}

	http.ServeFile(w, r, fullPath)
}

/*
 * handleWorkspaceWrite writes content to a file in the workspace directory.
 * desc: Accepts a JSON body with path and content fields. Creates parent
 *       directories if needed. Validates path is within workspace.
 */
func (a *API) handleWorkspaceWrite(w http.ResponseWriter, r *http.Request) {
	workspace := a.agent.Workspace()
	if workspace == "" {
		jsonError(w, "workspace not configured", http.StatusBadRequest)
		return
	}

	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		jsonError(w, "path required", http.StatusBadRequest)
		return
	}

	fullPath := filepath.Join(workspace, req.Path)
	if !strings.HasPrefix(filepath.Clean(fullPath), filepath.Clean(workspace)) {
		jsonError(w, "path outside workspace", http.StatusForbidden)
		return
	}

	// Create parent directories
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		jsonError(w, "create directory: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := os.WriteFile(fullPath, []byte(req.Content), 0644); err != nil {
		jsonError(w, "write file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]string{"status": "ok", "path": req.Path}, http.StatusOK)
}

// OneShotRequest is a raw completion request — a plain LLM call with no agent
// machinery (no preflight, planner, DAG, tools, reflection, or aggregator).
type OneShotRequest struct {
	Messages    []llm.Message `json:"messages"`
	Provider    string        `json:"provider,omitempty"`
	Model       string        `json:"model,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	// Images are https URLs or base64 data: URIs, attached to the most recent
	// user message as multimodal content (for a vision model). Supplied per
	// request by the host; not persisted here.
	Images []string `json:"images,omitempty"`
}

/*
 * handleOneShot runs a single provider-routed LLM completion, bypassing the
 * agent entirely. For hosts (e.g. makeen's compliance LLM-detection stage) that
 * need a raw completion routed through kaiju's provider keys, without paying for
 * the reasoning pipeline. Token usage is still attributed to the caller.
 */
func (a *API) handleOneShot(w http.ResponseWriter, r *http.Request) {
	var req OneShotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		jsonError(w, "messages is required", http.StatusBadRequest)
		return
	}
	// Bound untrusted knobs: a passthrough completion must not be able to request
	// an unbounded generation or an out-of-range temperature. 0 ⇒ a sane default.
	if req.MaxTokens <= 0 || req.MaxTokens > 8192 {
		req.MaxTokens = 1024
	}
	if req.Temperature < 0 {
		req.Temperature = 0
	} else if req.Temperature > 2 {
		req.Temperature = 2
	}
	// Multimodal: attach images to the latest user message. Bound the payload.
	if len(req.Images) > 0 {
		total := 0
		for _, img := range req.Images {
			total += len(img)
		}
		if total > 24*1024*1024 { // ~18 MB of image bytes as base64
			jsonError(w, "image payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		llm.AttachImages(req.Messages, req.Images)
	}
	var userID string
	if claims, ok := gateway.ClaimsFromContext(r.Context()); ok {
		userID = claims.Username
	}
	ctx := tokens.WithRun(tokens.WithPrincipal(r.Context(), userID))
	content, toks, err := a.agent.OneShot(ctx, req.Provider, req.Model, req.Messages, req.Temperature, req.MaxTokens)
	if err != nil {
		log.Printf("[api] oneshot error: %v", err)
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]any{"content": content, "tokens": toks}, http.StatusOK)
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
