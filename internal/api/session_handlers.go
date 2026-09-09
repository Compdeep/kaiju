package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/Compdeep/kaiju/internal/db"
	"github.com/Compdeep/kaiju/internal/gateway"
	"github.com/Compdeep/kaiju/internal/memory"
)

/*
 * handleEditMessage overwrites one message's content in a session the caller
 * owns. No LLM — a direct edit to the stored history, so a user can correct or
 * steer either their own message or the assistant's reply. Authorized strictly
 * by user id: the session must belong to the JWT principal, and the update is
 * scoped to (session, message) — a user can never edit another user's chat.
 */
func (a *API) handleEditMessage(w http.ResponseWriter, r *http.Request) {
	claims, ok := gateway.ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}
	sessionID := r.PathValue("id")
	// Ownership gate — the session must belong to THIS user.
	if _, err := a.db.GetSessionForUser(sessionID, claims.Username); err != nil {
		jsonError(w, "session not found", http.StatusNotFound)
		return
	}
	msgID, err := strconv.ParseInt(r.PathValue("msgId"), 10, 64)
	if err != nil || msgID <= 0 {
		jsonError(w, "invalid message id", http.StatusBadRequest)
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := a.db.UpdateMessageContent(sessionID, msgID, body.Content); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonResponse(w, map[string]string{"status": "updated"}, http.StatusOK)
}

/*
 * handleDeleteMessage removes a message (and everything after it) from a session
 * the caller owns. Used to delete the last message — including unsticking a
 * turn whose reply never came back — or to truncate a chat at a point. Same
 * user-id authorization as edit: a user can only delete from their own chat.
 */
func (a *API) handleDeleteMessage(w http.ResponseWriter, r *http.Request) {
	claims, ok := gateway.ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}
	sessionID := r.PathValue("id")
	if _, err := a.db.GetSessionForUser(sessionID, claims.Username); err != nil {
		jsonError(w, "session not found", http.StatusNotFound)
		return
	}
	msgID, err := strconv.ParseInt(r.PathValue("msgId"), 10, 64)
	if err != nil || msgID <= 0 {
		jsonError(w, "invalid message id", http.StatusBadRequest)
		return
	}
	if err := a.db.DeleteMessagesFrom(sessionID, msgID); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "deleted"}, http.StatusOK)
}

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
 * desc: Verifies session ownership, then returns the thread including messages a
 *       summary now stands for, paged by limit and offset.
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

	// The whole thread, compacted messages included, since this is what a person
	// reads rather than what a model is sent. Each row carries compacted_into, so
	// a summary can be shown in place of what it stands for and the reader can
	// still open it. Paged, because a long session is more than a view wants at
	// once — limit and offset are read from the query, with a ceiling so a large
	// value cannot ask for the whole of a very long conversation in one response.
	limit, offset := 200, 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 500 {
		limit = 500
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}
	msgs, err := a.db.GetFullTranscript(id, limit, offset)
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
 * handleSaveTrace saves a DAG execution trace onto the message it describes.
 * desc: Verifies session ownership, then persists the trace against the message
 *       the body names. The name is required: without one this saved onto the
 *       newest assistant message in the session, which is a different message as
 *       soon as anything else has answered.
 * param: w - HTTP response writer
 * param: r - HTTP request with JWT claims in context, an id path parameter, and a
 *            JSON body of {message_id, nodes}
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
		MessageID int64           `json:"message_id"`
		Nodes     json.RawMessage `json:"nodes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}

	// The message is named or the trace is refused.
	//
	// Without it this saved onto "the newest assistant message in the session",
	// which is a different message as soon as anything else has answered — a
	// second run, or a one-node interjection, replacing the trace of the run
	// that did the work. A client that does not know which message it watched
	// cannot say, and a guess is what caused that.
	//
	// The id comes back on the execute response as message_id.
	if req.MessageID == 0 {
		jsonError(w, "message_id is required: a trace belongs to one message", http.StatusBadRequest)
		return
	}

	// The session came from the path and was checked against the caller above,
	// so a message id from another conversation matches no row here.
	if err := a.db.SetDAGTrace(req.MessageID, id, string(req.Nodes)); err != nil {
		if errors.Is(err, db.ErrNoSuchMessage) {
			jsonError(w, "no such message in this session", http.StatusNotFound)
			return
		}
		jsonError(w, "failed to save trace", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]string{"status": "saved"}, http.StatusOK)
}
