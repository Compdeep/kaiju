package api

import (
	"encoding/json"
	"net/http"

	"github.com/Compdeep/kaiju/internal/clearance"
	"github.com/Compdeep/kaiju/internal/db"
)

/*
 * handleListClearanceEndpoints returns all configured clearance endpoints.
 * desc: Fetches the list of external approval endpoints from the database.
 * param: w - HTTP response writer
 */
func (a *API) handleListClearanceEndpoints(w http.ResponseWriter, _ *http.Request) {
	endpoints, err := a.db.ListClearanceEndpoints()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if endpoints == nil {
		endpoints = []db.ClearanceEndpoint{}
	}
	jsonResponse(w, endpoints, http.StatusOK)
}

/*
 * handleUpsertClearanceEndpoint creates or updates a clearance endpoint and live-updates the checker.
 * desc: Persists the endpoint to the database and registers it with the in-memory clearance checker.
 * param: w - HTTP response writer
 * param: r - HTTP request containing a db.ClearanceEndpoint JSON body
 */
func (a *API) handleUpsertClearanceEndpoint(w http.ResponseWriter, r *http.Request) {
	var ep db.ClearanceEndpoint
	if err := json.NewDecoder(r.Body).Decode(&ep); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if ep.ToolName == "" || ep.URL == "" {
		jsonError(w, "tool_name and url are required", http.StatusBadRequest)
		return
	}

	if err := a.db.UpsertClearanceEndpoint(ep); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Live-update the checker
	if a.clearanceChecker != nil {
		a.clearanceChecker.SetEndpoint(clearance.Endpoint{
			ToolName:  ep.ToolName,
			URL:       ep.URL,
			TimeoutMs: ep.TimeoutMs,
			Headers:   ep.Headers,
		})
	}

	jsonResponse(w, map[string]string{"status": "saved"}, http.StatusOK)
}

/*
 * handleDeleteClearanceEndpoint removes a clearance endpoint by tool name.
 * desc: Deletes the endpoint from the database and removes it from the in-memory clearance checker.
 * param: w - HTTP response writer
 * param: r - HTTP request with a tool path parameter
 */
func (a *API) handleDeleteClearanceEndpoint(w http.ResponseWriter, r *http.Request) {
	toolName := r.PathValue("tool")
	if err := a.db.DeleteClearanceEndpoint(toolName); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}

	if a.clearanceChecker != nil {
		a.clearanceChecker.RemoveEndpoint(toolName)
	}

	jsonResponse(w, map[string]string{"status": "deleted"}, http.StatusOK)
}
