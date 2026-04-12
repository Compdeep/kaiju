package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Compdeep/kaiju/internal/auth"
	"github.com/Compdeep/kaiju/internal/db"
	"github.com/Compdeep/kaiju/internal/gateway"
)

/*
 * AuthAPI handles authentication endpoints.
 * desc: Provides login and current-user info routes, backed by JWT tokens.
 */
type AuthAPI struct {
	db  *db.DB
	jwt *auth.JWTService
}

/*
 * NewAuthAPI creates auth API handlers.
 * desc: Constructs an AuthAPI wired to the database and JWT service.
 * param: database - database handle for user lookups and authentication
 * param: jwt - JWT service for issuing and validating tokens
 * return: a configured AuthAPI instance
 */
func NewAuthAPI(database *db.DB, jwt *auth.JWTService) *AuthAPI {
	return &AuthAPI{db: database, jwt: jwt}
}

/*
 * RegisterRoutes mounts auth routes. These are NOT JWT-protected.
 * desc: Registers the login and me endpoints on the given mux.
 * param: mux - the HTTP serve mux to attach routes to
 */
func (a *AuthAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", a.handleLogin)
	mux.HandleFunc("GET /api/v1/auth/me", a.handleMe)
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	Username  string `json:"username"`
	MaxIntent int    `json:"max_intent"`
}

/*
 * handleLogin authenticates a user and returns a signed JWT.
 * desc: Validates username/password credentials and issues a JWT token on success.
 * param: w - HTTP response writer
 * param: r - HTTP request containing a loginRequest JSON body
 */
func (a *AuthAPI) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Username == "" || req.Password == "" {
		jsonError(w, "username and password required", http.StatusBadRequest)
		return
	}

	dbUser, err := a.db.AuthenticateUser(req.Username, req.Password)
	if err != nil {
		jsonError(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	token, expires, err := a.jwt.Issue(dbUser)
	if err != nil {
		jsonError(w, "token generation failed", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, loginResponse{
		Token:     token,
		ExpiresAt: expires.Format(time.RFC3339),
		Username:  dbUser.Username,
		MaxIntent: dbUser.MaxIntent,
	}, http.StatusOK)
}

/*
 * handleMe returns the current authenticated user's profile.
 * desc: Extracts JWT claims from the request context and returns the user's username, max intent, and scopes.
 * param: w - HTTP response writer
 * param: r - HTTP request with JWT claims in context
 */
func (a *AuthAPI) handleMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := gateway.ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}

	dbUser, err := a.db.GetUser(claims.Username)
	if err != nil {
		jsonError(w, "user not found", http.StatusNotFound)
		return
	}

	type meResponse struct {
		Username  string   `json:"username"`
		MaxIntent int      `json:"max_intent"`
		Scopes    []string `json:"scopes"`
	}

	jsonResponse(w, meResponse{
		Username:  dbUser.Username,
		MaxIntent: dbUser.MaxIntent,
		Scopes:    dbUser.Scopes,
	}, http.StatusOK)
}
