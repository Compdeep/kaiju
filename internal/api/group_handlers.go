package api

import (
	"encoding/json"
	"net/http"

	"github.com/Compdeep/kaiju/internal/db"
)

/*
 * GroupAPI handles group CRUD endpoints.
 * desc: Provides list, create, update, and delete operations for user groups.
 */
type GroupAPI struct {
	db *db.DB
}

/*
 * NewGroupAPI creates group API handlers.
 * desc: Constructs a GroupAPI wired to the database.
 * param: database - database handle for group persistence
 * return: a configured GroupAPI instance
 */
func NewGroupAPI(database *db.DB) *GroupAPI {
	return &GroupAPI{db: database}
}

/*
 * RegisterRoutes mounts group CRUD routes on the given mux.
 * desc: Registers list, create, update, and delete group endpoints.
 * param: mux - the HTTP serve mux to attach routes to
 */
func (g *GroupAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/groups", g.handleList)
	mux.HandleFunc("POST /api/v1/groups", g.handleCreate)
	mux.HandleFunc("PUT /api/v1/groups/{name}", g.handleUpdate)
	mux.HandleFunc("DELETE /api/v1/groups/{name}", g.handleDelete)
}

/*
 * handleList returns all groups.
 * desc: Fetches the full group list from the database and returns it as JSON.
 * param: w - HTTP response writer
 */
func (g *GroupAPI) handleList(w http.ResponseWriter, _ *http.Request) {
	groups, err := g.db.ListGroups()
	if err != nil {
		jsonError(w, "failed to list groups", http.StatusInternalServerError)
		return
	}
	if groups == nil {
		groups = []db.Group{}
	}
	jsonResponse(w, groups, http.StatusOK)
}

/*
 * handleCreate creates a new group.
 * desc: Decodes a Group from the request body and inserts it into the database.
 * param: w - HTTP response writer
 * param: r - HTTP request containing a db.Group JSON body
 */
func (g *GroupAPI) handleCreate(w http.ResponseWriter, r *http.Request) {
	var group db.Group
	if err := json.NewDecoder(r.Body).Decode(&group); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if group.Name == "" {
		jsonError(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := g.db.CreateGroup(group); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)
		return
	}
	jsonResponse(w, group, http.StatusCreated)
}

/*
 * handleUpdate replaces a group by name.
 * desc: Decodes a Group from the request body and updates the matching record in the database.
 * param: w - HTTP response writer
 * param: r - HTTP request containing a db.Group JSON body and a name path parameter
 */
func (g *GroupAPI) handleUpdate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var group db.Group
	if err := json.NewDecoder(r.Body).Decode(&group); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := g.db.UpdateGroup(name, group); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonResponse(w, map[string]string{"status": "updated"}, http.StatusOK)
}

/*
 * handleDelete removes a group by name.
 * desc: Deletes the group identified by the name path parameter from the database.
 * param: w - HTTP response writer
 * param: r - HTTP request with a name path parameter
 */
func (g *GroupAPI) handleDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := g.db.DeleteGroup(name); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonResponse(w, map[string]string{"status": "deleted"}, http.StatusOK)
}
