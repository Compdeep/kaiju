package api

import (
	"encoding/json"
	"net/http"

	"github.com/user/kaiju/internal/db"
)

/*
 * ScopeAPI handles scope CRUD endpoints.
 * desc: Provides list, create, update, and delete operations for tool access scopes.
 */
type ScopeAPI struct {
	db *db.DB
}

/*
 * NewScopeAPI creates scope API handlers.
 * desc: Constructs a ScopeAPI wired to the database.
 * param: database - database handle for scope persistence
 * return: a configured ScopeAPI instance
 */
func NewScopeAPI(database *db.DB) *ScopeAPI {
	return &ScopeAPI{db: database}
}

/*
 * RegisterRoutes mounts scope CRUD routes on the given mux.
 * desc: Registers list, create, update, and delete scope endpoints.
 * param: mux - the HTTP serve mux to attach routes to
 */
func (s *ScopeAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/scopes", s.handleList)
	mux.HandleFunc("POST /api/v1/scopes", s.handleCreate)
	mux.HandleFunc("PUT /api/v1/scopes/{name}", s.handleUpdate)
	mux.HandleFunc("DELETE /api/v1/scopes/{name}", s.handleDelete)
}

/*
 * handleList returns all scopes.
 * desc: Fetches the full scope list from the database and returns it as JSON.
 * param: w - HTTP response writer
 */
func (s *ScopeAPI) handleList(w http.ResponseWriter, _ *http.Request) {
	scopes, err := s.db.ListScopes()
	if err != nil {
		jsonError(w, "failed to list scopes", http.StatusInternalServerError)
		return
	}
	if scopes == nil {
		scopes = []db.Scope{}
	}
	jsonResponse(w, scopes, http.StatusOK)
}

/*
 * handleCreate creates a new scope.
 * desc: Decodes a Scope from the request body and inserts it into the database.
 * param: w - HTTP response writer
 * param: r - HTTP request containing a db.Scope JSON body
 */
func (s *ScopeAPI) handleCreate(w http.ResponseWriter, r *http.Request) {
	var scope db.Scope
	if err := json.NewDecoder(r.Body).Decode(&scope); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if scope.Name == "" {
		jsonError(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := s.db.CreateScope(scope); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)
		return
	}
	jsonResponse(w, scope, http.StatusCreated)
}

/*
 * handleUpdate replaces a scope by name.
 * desc: Decodes a Scope from the request body and updates the matching record in the database.
 * param: w - HTTP response writer
 * param: r - HTTP request containing a db.Scope JSON body and a name path parameter
 */
func (s *ScopeAPI) handleUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var scope db.Scope
	if err := json.NewDecoder(r.Body).Decode(&scope); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := s.db.UpdateScope(name, scope); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonResponse(w, map[string]string{"status": "updated"}, http.StatusOK)
}

/*
 * handleDelete removes a scope by name.
 * desc: Deletes the scope identified by the name path parameter from the database.
 * param: w - HTTP response writer
 * param: r - HTTP request with a name path parameter
 */
func (s *ScopeAPI) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.db.DeleteScope(name); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonResponse(w, map[string]string{"status": "deleted"}, http.StatusOK)
}
