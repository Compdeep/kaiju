package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Compdeep/kaiju/internal/db"
	"github.com/Compdeep/kaiju/internal/gateway"
	"github.com/Compdeep/kaiju/internal/memory"
)

/*
 * handleCreateSession creates a new conversation session for the authenticated user.
 * desc: Extracts the user from JWT claims and creates a new session via the memory manager.
 * param: w - HTTP response writer
 * param: r - HTTP request with JWT claims in context
 */
func (a *API) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	claims, ok := gateway.ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	memMgr := memory.New(a.db, a.llmClient, claims.Username)
	id, err := memMgr.NewSession(r.Context(), "web")
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"id": id}, http.StatusCreated)
}

/*
 * handleListSessions returns the authenticated user's conversation sessions.
 * desc: Lists up to 50 sessions for the current user, ordered newest first.
 * param: w - HTTP response writer
 * param: r - HTTP request with JWT claims in context
 */
func (a *API) handleListSessions(w http.ResponseWriter, r *http.Request) {
	claims, ok := gateway.ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	memMgr := memory.New(a.db, a.llmClient, claims.Username)
	sessions, err := memMgr.ListSessions(50)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if sessions == nil {
		sessions = []db.Session{}
	}
	jsonResponse(w, sessions, http.StatusOK)
}

/*
 * handleDeleteSession removes a conversation session owned by the authenticated user.
 * desc: Verifies ownership via the memory manager and deletes the session and its messages.
 * param: w - HTTP response writer
 * param: r - HTTP request with JWT claims in context and an id path parameter
 */
func (a *API) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	claims, ok := gateway.ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	id := r.PathValue("id")
	memMgr := memory.New(a.db, a.llmClient, claims.Username)
	if err := memMgr.DeleteSession(id); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonResponse(w, map[string]string{"status": "deleted"}, http.StatusOK)
}

/*
 * handleGetMessages returns messages for a session owned by the authenticated user.
 * desc: Verifies session ownership, then returns up to 500 messages including DAG trace entries.
 * param: w - HTTP response writer
 * param: r - HTTP request with JWT claims in context and an id path parameter
 */
func (a *API) handleGetMessages(w http.ResponseWriter, r *http.Request) {
	claims, ok := gateway.ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	id := r.PathValue("id")

	// Verify ownership
	if _, err := a.db.GetSessionForUser(id, claims.Username); err != nil {
		jsonError(w, "session not found", http.StatusNotFound)
		return
	}

	// Return raw db messages (includes dag_trace entries)
	msgs, err := a.db.GetMessages(id, 500)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if msgs == nil {
		msgs = []db.Message{}
	}
	jsonResponse(w, msgs, http.StatusOK)
}

/*
 * handleCompactSession summarizes old messages in a session to reduce context size.
 * desc: Triggers LLM-based compaction on the session, replacing old messages with a summary.
 * param: w - HTTP response writer
 * param: r - HTTP request with JWT claims in context and an id path parameter
 */
func (a *API) handleCompactSession(w http.ResponseWriter, r *http.Request) {
	claims, ok := gateway.ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	id := r.PathValue("id")
	memMgr := memory.New(a.db, a.llmClient, claims.Username)
	summary, err := memMgr.Compact(context.Background(), id)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"summary": summary}, http.StatusOK)
}

/*
 * handleSaveTrace saves a DAG execution trace to the most recent assistant message in a session.
 * desc: Verifies session ownership, then persists the provided DAG node trace JSON.
 * param: w - HTTP response writer
 * param: r - HTTP request with JWT claims in context, an id path parameter, and a JSON body with nodes
 */
func (a *API) handleSaveTrace(w http.ResponseWriter, r *http.Request) {
	claims, ok := gateway.ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	id := r.PathValue("id")

	// Verify ownership
	_, err := a.db.GetSessionForUser(id, claims.Username)
	if err != nil {
		jsonError(w, "session not found", http.StatusNotFound)
		return
	}

	var req struct {
		Nodes json.RawMessage `json:"nodes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}

	// Save trace on the most recent assistant message in this session
	if err := a.db.SetDAGTrace(id, string(req.Nodes)); err != nil {
		jsonError(w, "failed to save trace", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]string{"status": "saved"}, http.StatusOK)
}
