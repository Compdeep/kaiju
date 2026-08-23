// Package configapi serves kaiju's own configuration file over HTTP.
//
// Separate from internal/api because it is the daemon's business, not the
// interface's: it reads and writes the file kaiju was started with, which an
// application embedding the interface does not have and would not want edited.
// Keeping it here is also what lets the ui package import internal/api at all —
// internal/config imports ui for the interface's own settings, so an api that
// knew the daemon's configuration shape made a cycle.
package configapi

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/internal/config"
	"github.com/Compdeep/kaiju/models"
)

/*
 * API handles reading and updating the kaiju config from the UI.
 * desc: Provides GET/PATCH for runtime configuration and a model catalog endpoint.
 */
type API struct {
	cfg     *config.Config
	cfgPath string
	agent   *agent.Agent // for live-updating LLM client
}

/*
 * NewAPI creates config API handlers.
 * desc: Constructs a API wired to the live config, its disk path, and the agent for live-reload.
 * param: cfg - pointer to the live configuration struct
 * param: cfgPath - filesystem path where the config JSON is persisted
 * param: ag - agent instance for live-updating LLM and executor clients
 * return: a configured API instance
 */
func New(cfg *config.Config, cfgPath string, ag *agent.Agent) *API {
	return &API{cfg: cfg, cfgPath: cfgPath, agent: ag}
}

/*
 * RegisterRoutes mounts config routes on the mux.
 * desc: Registers get-config, update-config, and list-models endpoints.
 * param: mux - the HTTP serve mux to attach routes to
 */
func (c *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/config", c.handleGetConfig)
	mux.HandleFunc("PATCH /api/v1/config", c.handleUpdateConfig)
	mux.HandleFunc("GET /api/v1/models", c.handleListModels)
}

/*
 * handleGetConfig returns the current configuration with secrets masked.
 * desc: Copies the config, masks the API key and JWT secret, and returns it as JSON.
 * param: w - HTTP response writer
 */
func (c *API) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	// Return config with API key masked
	safe := *c.cfg
	if len(safe.LLM.APIKey) > 8 {
		safe.LLM.APIKey = safe.LLM.APIKey[:4] + "****" + safe.LLM.APIKey[len(safe.LLM.APIKey)-4:]
	} else if safe.LLM.APIKey != "" {
		safe.LLM.APIKey = "****"
	}
	safe.API.JWTSecret = ""
	safe.API.AuthToken = ""
	jsonResponse(w, safe, http.StatusOK)
}

/*
 * configPatch is a partial config update — only provided fields are applied.
 * desc: Nullable fields allow callers to update individual config values without overwriting others.
 */
type configPatch struct {
	LLM *struct {
		Provider    *string  `json:"provider,omitempty"`
		Endpoint    *string  `json:"endpoint,omitempty"`
		APIKey      *string  `json:"api_key,omitempty"`
		Model       *string  `json:"model,omitempty"`
		Temperature *float64 `json:"temperature,omitempty"`
		MaxTokens   *int     `json:"max_tokens,omitempty"`
	} `json:"llm,omitempty"`
	Executor *struct {
		Provider *string `json:"provider,omitempty"`
		Endpoint *string `json:"endpoint,omitempty"`
		APIKey   *string `json:"api_key,omitempty"`
		Model    *string `json:"model,omitempty"`
	} `json:"executor,omitempty"`
	Vision *struct {
		Provider *string `json:"provider,omitempty"`
		Model    *string `json:"model,omitempty"`
	} `json:"vision,omitempty"`
	Chat *struct {
		Provider *string   `json:"provider,omitempty"`
		Model    *string   `json:"model,omitempty"`
		Tools    *[]string `json:"tools,omitempty"`
	} `json:"chat,omitempty"`
	Agent *struct {
		DAGEnabled        *bool   `json:"dag_enabled,omitempty"`
		DAGMode           *string `json:"dag_mode,omitempty"`
		SafetyLevel       *int    `json:"safety_level,omitempty"`
		MaxInvestigations *int    `json:"max_investigations,omitempty"`
		MaxReplans        *int    `json:"max_replans,omitempty"`
		RouteProvider     *string `json:"route_provider,omitempty"`
		RouteModel        *string `json:"route_model,omitempty"`
		AnswerProvider    *string `json:"answer_provider,omitempty"`
		AnswerModel       *string `json:"answer_model,omitempty"`
	} `json:"agent,omitempty"`
}

/*
 * handleUpdateConfig applies a partial config patch, live-updates the agent, and saves to disk.
 * desc: Merges non-nil fields from the patch into the live config, hot-reloads LLM/executor clients, and persists the result.
 * param: w - HTTP response writer
 * param: r - HTTP request containing a configPatch JSON body
 */
func (c *API) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var patch configPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Apply LLM patches
	if patch.LLM != nil {
		if patch.LLM.Provider != nil {
			c.cfg.LLM.Provider = *patch.LLM.Provider
		}
		if patch.LLM.Endpoint != nil {
			c.cfg.LLM.Endpoint = *patch.LLM.Endpoint
		}
		if patch.LLM.APIKey != nil {
			c.cfg.LLM.APIKey = *patch.LLM.APIKey
		}
		if patch.LLM.Model != nil {
			c.cfg.LLM.Model = *patch.LLM.Model
		}
		if patch.LLM.Temperature != nil {
			c.cfg.LLM.Temperature = *patch.LLM.Temperature
		}
		if patch.LLM.MaxTokens != nil {
			c.cfg.LLM.MaxTokens = *patch.LLM.MaxTokens
		}
	}

	// Apply agent patches
	if patch.Agent != nil {
		if patch.Agent.DAGEnabled != nil {
			c.cfg.Agent.DAGEnabled = *patch.Agent.DAGEnabled
			c.agent.SetDAGEnabled(*patch.Agent.DAGEnabled)
		}
		if patch.Agent.DAGMode != nil {
			c.cfg.Agent.DAGMode = *patch.Agent.DAGMode
		}
		if patch.Agent.SafetyLevel != nil {
			c.cfg.Agent.SafetyLevel = *patch.Agent.SafetyLevel
		}
		if patch.Agent.MaxInvestigations != nil {
			c.cfg.Agent.MaxInvestigations = *patch.Agent.MaxInvestigations
		}
		if patch.Agent.MaxReplans != nil {
			c.cfg.Agent.MaxReplans = *patch.Agent.MaxReplans
		}
		if patch.Agent.RouteProvider != nil {
			c.cfg.Agent.RouteProvider = *patch.Agent.RouteProvider
		}
		if patch.Agent.RouteModel != nil {
			c.cfg.Agent.RouteModel = *patch.Agent.RouteModel
		}
		if patch.Agent.RouteProvider != nil || patch.Agent.RouteModel != nil {
			c.agent.SetRouteModel(c.cfg.Agent.RouteProvider, c.cfg.Agent.RouteModel)
			log.Printf("[config] route model updated: provider=%s model=%s", c.cfg.Agent.RouteProvider, c.cfg.Agent.RouteModel)
		}
		if patch.Agent.AnswerProvider != nil {
			c.cfg.Agent.AnswerProvider = *patch.Agent.AnswerProvider
		}
		if patch.Agent.AnswerModel != nil {
			c.cfg.Agent.AnswerModel = *patch.Agent.AnswerModel
		}
		if patch.Agent.AnswerProvider != nil || patch.Agent.AnswerModel != nil {
			c.agent.SetAnswerModel(c.cfg.Agent.AnswerProvider, c.cfg.Agent.AnswerModel)
			log.Printf("[config] answer model updated: provider=%s model=%s", c.cfg.Agent.AnswerProvider, c.cfg.Agent.AnswerModel)
		}
	}

	// Apply executor patches
	if patch.Executor != nil {
		if patch.Executor.Provider != nil {
			c.cfg.Executor.Provider = *patch.Executor.Provider
		}
		if patch.Executor.Endpoint != nil {
			c.cfg.Executor.Endpoint = *patch.Executor.Endpoint
		}
		if patch.Executor.APIKey != nil {
			c.cfg.Executor.APIKey = *patch.Executor.APIKey
		}
		if patch.Executor.Model != nil {
			c.cfg.Executor.Model = *patch.Executor.Model
		}
	}

	// Apply vision patches (live-applied to the agent below)
	if patch.Vision != nil {
		if patch.Vision.Provider != nil {
			c.cfg.Vision.Provider = *patch.Vision.Provider
		}
		if patch.Vision.Model != nil {
			c.cfg.Vision.Model = *patch.Vision.Model
		}
		if c.agent != nil {
			c.agent.SetVisionModel(c.cfg.Vision.Provider, c.cfg.Vision.Model)
			log.Printf("[config] vision model updated: provider=%s model=%s", c.cfg.Vision.Provider, c.cfg.Vision.Model)
		}
	}

	// Apply chat patches (live-applied to the agent)
	if patch.Chat != nil {
		if patch.Chat.Provider != nil {
			c.cfg.Chat.Provider = *patch.Chat.Provider
		}
		if patch.Chat.Model != nil {
			c.cfg.Chat.Model = *patch.Chat.Model
		}
		if patch.Chat.Tools != nil {
			c.cfg.Chat.Tools = *patch.Chat.Tools
		}
		if c.agent != nil {
			c.agent.SetChatModel(c.cfg.Chat.Provider, c.cfg.Chat.Model)
			// c.cfg.Chat.Tools is kept and served back, and no longer pushed at
			// the agent: nothing there read it. A request carries its own
			// chat_tools, which is what the chat lane uses.
			log.Printf("[config] chat updated: provider=%s model=%s", c.cfg.Chat.Provider, c.cfg.Chat.Model)
		}
	}

	// Live-update the agent's LLM clients when config changes
	if patch.LLM != nil && c.agent != nil {
		c.agent.SetLLMClient(c.cfg.LLM.Provider, c.cfg.LLM.Endpoint, c.cfg.LLM.APIKey, c.cfg.LLM.Model)
		log.Printf("[config] reasoning model updated: provider=%s model=%s", c.cfg.LLM.Provider, c.cfg.LLM.Model)
	}
	if patch.Executor != nil && c.agent != nil && c.cfg.Executor.Model != "" {
		ep := c.cfg.Executor.Endpoint
		if ep == "" {
			ep = c.cfg.LLM.Endpoint
		}
		prov := c.cfg.Executor.Provider
		if prov == "" {
			prov = c.cfg.LLM.Provider
		}
		key := c.cfg.Executor.APIKey
		if key == "" {
			key = c.cfg.LLM.APIKey
		}
		c.agent.SetExecutorClient(prov, ep, key, c.cfg.Executor.Model)
		log.Printf("[config] executor model updated: model=%s", c.cfg.Executor.Model)
	}

	// Save to disk
	if c.cfgPath != "" {
		if err := c.saveToDisk(); err != nil {
			log.Printf("[config] save error: %v", err)
			jsonError(w, "config updated in memory but failed to save: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	jsonResponse(w, map[string]string{"status": "updated"}, http.StatusOK)
}

/*
 * saveToDisk persists the current config to the JSON file on disk.
 * desc: Marshals the config as indented JSON and writes it to the configured cfgPath.
 * return: an error if marshaling or writing fails
 */
func (c *API) saveToDisk() error {
	data, err := json.MarshalIndent(c.cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.cfgPath, data, 0600)
}

// ─── Model Catalog ──────────────────────────────────────────────────────────
//
// The catalog itself now lives in package models, outside internal/, so an
// application embedding kaiju can read the same list instead of keeping a
// second hand-maintained copy. This file only serves it.

/*
 * handleListModels returns the supported model catalog.
 * desc: Returns the static list of all known LLM models across providers,
 *       each marked available or not for this host's configured keys.
 * param: w - HTTP response writer
 */
func (c *API) handleListModels(w http.ResponseWriter, _ *http.Request) {
	// Mark each model available iff its provider is configured with a key.
	// The legacy single-provider config (llm.provider) also counts, so hosts
	// that haven't adopted the providers block still see their models.
	configured := make(map[string]bool, len(c.cfg.Providers)+1)
	for name, p := range c.cfg.Providers {
		if p.APIKey != "" {
			configured[name] = true
		}
	}
	if c.cfg.LLM.Provider != "" && c.cfg.LLM.APIKey != "" {
		configured[c.cfg.LLM.Provider] = true
	}
	out := models.All()
	for i := range out {
		out[i].Available = configured[out[i].Provider]
	}
	jsonResponse(w, out, http.StatusOK)
}

/*
 * ModelLimits reports what a model can take in and give back, in tokens.
 * desc: Kept as a shim so cmd/kaiju keeps one import for its agent config.
 *       models.Limits is the implementation.
 * param: id - the model id as configured for a lane, e.g. "openai/gpt-4.1".
 * return: the context window and the largest reply the provider will produce.
 */
func ModelLimits(id string) (contextTokens, maxOutputTokens int) {
	return models.Limits(id)
}

/*
 * jsonResponse writes a value as JSON with the given status.
 * desc: A copy of the helper this file used while it lived in package api.
 *       Copying six lines is cheaper than exporting a helper from one package
 *       so another can format a response the same way.
 * param: w - the response.
 * param: data - what to encode.
 * param: status - the HTTP status.
 */
func jsonResponse(w http.ResponseWriter, data any, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

/*
 * jsonError writes an error as JSON with the given status.
 * param: w - the response.
 * param: msg - what went wrong.
 * param: status - the HTTP status.
 */
func jsonError(w http.ResponseWriter, msg string, status int) {
	jsonResponse(w, map[string]string{"error": msg}, status)
}
