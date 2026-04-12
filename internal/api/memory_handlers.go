package api

import (
	"encoding/json"
	"net/http"

	"github.com/Compdeep/kaiju/internal/db"
	"github.com/Compdeep/kaiju/internal/gateway"
	"github.com/Compdeep/kaiju/internal/memory"
)

/*
 * storeMemoryRequest is the payload for POST /api/v1/memories.
 * desc: Contains the key, content, type, and tags for a long-term memory entry.
 */
type storeMemoryRequest struct {
	Key     string   `json:"key"`
	Content string   `json:"content"`
	Type    string   `json:"type"` // "semantic" or "episodic"
	Tags    []string `json:"tags"`
}

/*
 * handleStoreMemory persists a long-term memory entry for the authenticated user.
 * desc: Decodes the memory payload, defaults type to semantic if empty, and stores it scoped to the user.
 * param: w - HTTP response writer
 * param: r - HTTP request with JWT claims in context and a storeMemoryRequest JSON body
 */
func (a *API) handleStoreMemory(w http.ResponseWriter, r *http.Request) {
	claims, ok := gateway.ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	var req storeMemoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Key == "" || req.Content == "" {
		jsonError(w, "key and content are required", http.StatusBadRequest)
		return
	}
	if req.Type == "" {
		req.Type = memory.TypeSemantic
	}

	memMgr := memory.New(a.db, a.llmClient, claims.Username)
	if err := memMgr.StoreMemory(req.Key, req.Content, req.Type, req.Tags); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "stored"}, http.StatusCreated)
}

/*
 * handleSearchMemories searches the authenticated user's long-term memories.
 * desc: Filters by optional query string and memory type, returning up to 50 results.
 * param: w - HTTP response writer
 * param: r - HTTP request with JWT claims in context and optional q/type query parameters
 */
func (a *API) handleSearchMemories(w http.ResponseWriter, r *http.Request) {
	claims, ok := gateway.ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	query := r.URL.Query().Get("q")
	memType := r.URL.Query().Get("type")

	var types []string
	if memType != "" {
		types = []string{memType}
	}

	memMgr := memory.New(a.db, a.llmClient, claims.Username)
	memories, err := memMgr.RecallMemories(query, types, 50)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if memories == nil {
		memories = []db.Memory{}
	}
	jsonResponse(w, memories, http.StatusOK)
}

/*
 * handleDeleteMemory removes a long-term memory entry owned by the authenticated user.
 * desc: Verifies ownership via namespace prefix and deletes the memory by ID.
 * param: w - HTTP response writer
 * param: r - HTTP request with JWT claims in context and an id path parameter
 */
func (a *API) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	claims, ok := gateway.ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	id := r.PathValue("id")
	memMgr := memory.New(a.db, a.llmClient, claims.Username)
	if err := memMgr.ForgetMemory(id); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonResponse(w, map[string]string{"status": "deleted"}, http.StatusOK)
}
