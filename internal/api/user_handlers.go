package api

import (
	"encoding/json"
	"net/http"

	"github.com/user/kaiju/internal/db"
)

/*
 * UserAPI handles user CRUD endpoints.
 * desc: Provides list, create, update, and delete operations for user accounts.
 */
type UserAPI struct {
	db *db.DB
}

/*
 * NewUserAPI creates user API handlers.
 * desc: Constructs a UserAPI wired to the database.
 * param: database - database handle for user persistence
 * return: a configured UserAPI instance
 */
func NewUserAPI(database *db.DB) *UserAPI {
	return &UserAPI{db: database}
}

/*
 * RegisterRoutes mounts user CRUD routes on the given mux.
 * desc: Registers list, create, update, and delete user endpoints.
 * param: mux - the HTTP serve mux to attach routes to
 */
func (u *UserAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/users", u.handleList)
	mux.HandleFunc("POST /api/v1/users", u.handleCreate)
	mux.HandleFunc("PUT /api/v1/users/{username}", u.handleUpdate)
	mux.HandleFunc("DELETE /api/v1/users/{username}", u.handleDelete)
}

/*
 * handleList returns all users.
 * desc: Fetches the full user list from the database and returns it as JSON.
 * param: w - HTTP response writer
 */
func (u *UserAPI) handleList(w http.ResponseWriter, _ *http.Request) {
	users, err := u.db.ListUsers()
	if err != nil {
		jsonError(w, "failed to list users", http.StatusInternalServerError)
		return
	}
	if users == nil {
		users = []db.User{}
	}
	jsonResponse(w, users, http.StatusOK)
}

type createUserRequest struct {
	Username  string   `json:"username"`
	Password  string   `json:"password"`
	MaxIntent int      `json:"max_intent"`
	Scopes    []string `json:"scopes"`
}

/*
 * handleCreate creates a new user account.
 * desc: Decodes a createUserRequest and inserts the user into the database.
 * param: w - HTTP response writer
 * param: r - HTTP request containing a createUserRequest JSON body
 */
func (u *UserAPI) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Username == "" || req.Password == "" {
		jsonError(w, "username and password are required", http.StatusBadRequest)
		return
	}
	if err := u.db.CreateUser(req.Username, req.Password, req.MaxIntent, req.Scopes); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)
		return
	}
	jsonResponse(w, map[string]string{"status": "created", "username": req.Username}, http.StatusCreated)
}

type updateUserRequest struct {
	MaxIntent *int     `json:"max_intent,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
	Groups    []string `json:"groups,omitempty"`
	Disabled  *bool    `json:"disabled,omitempty"`
}

/*
 * handleUpdate applies partial updates to an existing user.
 * desc: Merges the provided fields with the existing user record and persists the result.
 * param: w - HTTP response writer
 * param: r - HTTP request containing an updateUserRequest JSON body and a username path parameter
 */
func (u *UserAPI) handleUpdate(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")

	existing, err := u.db.GetUser(username)
	if err != nil {
		jsonError(w, "user not found", http.StatusNotFound)
		return
	}

	var req updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	maxIntent := existing.MaxIntent
	if req.MaxIntent != nil {
		maxIntent = *req.MaxIntent
	}
	scopes := existing.Scopes
	if req.Scopes != nil {
		scopes = req.Scopes
	}
	groups := existing.Groups
	if req.Groups != nil {
		groups = req.Groups
	}
	disabled := existing.Disabled
	if req.Disabled != nil {
		disabled = *req.Disabled
	}

	if err := u.db.UpdateUser(username, maxIntent, scopes, groups, disabled); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "updated"}, http.StatusOK)
}

/*
 * handleDelete removes a user by username.
 * desc: Deletes the user identified by the username path parameter from the database.
 * param: w - HTTP response writer
 * param: r - HTTP request with a username path parameter
 */
func (u *UserAPI) handleDelete(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	if err := u.db.DeleteUser(username); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonResponse(w, map[string]string{"status": "deleted"}, http.StatusOK)
}
