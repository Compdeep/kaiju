package api

import (
	"encoding/json"
	"net/http"

	"github.com/user/kaiju/internal/agent"
	"github.com/user/kaiju/internal/db"
)

/*
 * IntentAPI handles CRUD endpoints for intents and tool_intent assignments.
 * desc: Intents are configurable IBE levels with sparse integer ranks and
 *       per-intent prompt descriptions. Builtins cannot be deleted and their
 *       rank cannot change. Tool assignments override a tool's Go Impact()
 *       default with a named intent.
 */
type IntentAPI struct {
	db    *db.DB
	agent *agent.Agent
}

/*
 * NewIntentAPI creates intent API handlers.
 * param: database - DB handle.
 * param: ag - agent (used to list registered tools).
 */
func NewIntentAPI(database *db.DB, ag *agent.Agent) *IntentAPI {
	return &IntentAPI{db: database, agent: ag}
}

/*
 * RegisterRoutes mounts CRUD routes for intents and tool_intents.
 */
func (a *IntentAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/intents", a.handleListIntents)
	mux.HandleFunc("POST /api/v1/intents", a.handleCreateIntent)
	mux.HandleFunc("PUT /api/v1/intents/{name}", a.handleUpdateIntent)
	mux.HandleFunc("DELETE /api/v1/intents/{name}", a.handleDeleteIntent)

	mux.HandleFunc("GET /api/v1/tool-intents", a.handleListToolIntents)
	mux.HandleFunc("PUT /api/v1/tool-intents/{tool}", a.handleSetToolIntent)
	mux.HandleFunc("DELETE /api/v1/tool-intents/{tool}", a.handleDeleteToolIntent)
}

// ── Intents CRUD ──

func (a *IntentAPI) handleListIntents(w http.ResponseWriter, _ *http.Request) {
	intents, err := a.db.ListIntents()
	if err != nil {
		jsonError(w, "failed to list intents", http.StatusInternalServerError)
		return
	}
	if intents == nil {
		intents = []db.Intent{}
	}
	jsonResponse(w, intents, http.StatusOK)
}

func (a *IntentAPI) handleCreateIntent(w http.ResponseWriter, r *http.Request) {
	var intent db.Intent
	if err := json.NewDecoder(r.Body).Decode(&intent); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if intent.Name == "" {
		jsonError(w, "name is required", http.StatusBadRequest)
		return
	}
	// New intents are never builtin — builtin flag is controlled by seeding only
	intent.IsBuiltin = false
	if err := a.db.CreateIntent(intent); err != nil {
		jsonError(w, err.Error(), http.StatusConflict)
		return
	}
	jsonResponse(w, intent, http.StatusCreated)
}

func (a *IntentAPI) handleUpdateIntent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var intent db.Intent
	if err := json.NewDecoder(r.Body).Decode(&intent); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := a.db.UpdateIntent(name, intent); err != nil {
		jsonError(w, err.Error(), http.StatusNotFound)
		return
	}
	jsonResponse(w, map[string]string{"status": "updated", "note": "restart required for changes to take effect"}, http.StatusOK)
}

func (a *IntentAPI) handleDeleteIntent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := a.db.DeleteIntent(name); err != nil {
		status := http.StatusNotFound
		if err.Error() == "intent \""+name+"\" is builtin and cannot be deleted" {
			status = http.StatusForbidden
		}
		jsonError(w, err.Error(), status)
		return
	}
	jsonResponse(w, map[string]string{"status": "deleted", "note": "restart required for changes to take effect"}, http.StatusOK)
}

// ── Tool intent assignments ──

/*
 * ToolIntentEntry is what GET /api/v1/tool-intents returns per row.
 * desc: Includes the tool name, current intent assignment (or "" if none),
 *       and the default Go intent for reference in the UI.
 */
type ToolIntentEntry struct {
	ToolName        string `json:"tool_name"`
	IntentName      string `json:"intent_name"` // empty if no DB override
	DefaultIntent   string `json:"default_intent"` // resolved from Go Impact()
	HasOverride     bool   `json:"has_override"`
}

func (a *IntentAPI) handleListToolIntents(w http.ResponseWriter, _ *http.Request) {
	overrides, err := a.db.ListToolIntents()
	if err != nil {
		jsonError(w, "failed to list tool intents", http.StatusInternalServerError)
		return
	}

	registry := a.agent.Intents()
	toolNames := a.agent.Registry().List()
	entries := make([]ToolIntentEntry, 0, len(toolNames))

	for _, name := range toolNames {
		tool, ok := a.agent.Registry().Get(name)
		if !ok {
			continue
		}
		entry := ToolIntentEntry{
			ToolName: name,
		}
		if override, hasOverride := overrides[name]; hasOverride {
			entry.IntentName = override
			entry.HasOverride = true
		}
		// Default from compiled Go Impact() — bypass the pin so the UI
		// can show what the tool would resolve to without an assignment.
		defaultRank := tool.Impact(nil)
		entry.DefaultIntent = registry.NameByRank(defaultRank)
		if entry.IntentName == "" {
			entry.IntentName = entry.DefaultIntent
		}
		entries = append(entries, entry)
	}

	jsonResponse(w, entries, http.StatusOK)
}

func (a *IntentAPI) handleSetToolIntent(w http.ResponseWriter, r *http.Request) {
	toolName := r.PathValue("tool")
	var body struct {
		IntentName string `json:"intent_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.IntentName == "" {
		jsonError(w, "intent_name is required", http.StatusBadRequest)
		return
	}
	// Validate intent exists
	if _, err := a.db.GetIntent(body.IntentName); err != nil {
		jsonError(w, "intent not found: "+body.IntentName, http.StatusBadRequest)
		return
	}
	// Validate tool exists
	if _, ok := a.agent.Registry().Get(toolName); !ok {
		jsonError(w, "tool not found: "+toolName, http.StatusBadRequest)
		return
	}
	if err := a.db.SetToolIntent(toolName, body.IntentName); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "updated", "note": "restart required for changes to take effect"}, http.StatusOK)
}

func (a *IntentAPI) handleDeleteToolIntent(w http.ResponseWriter, r *http.Request) {
	toolName := r.PathValue("tool")
	if err := a.db.DeleteToolIntent(toolName); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]string{"status": "deleted", "note": "restart required for changes to take effect"}, http.StatusOK)
}
