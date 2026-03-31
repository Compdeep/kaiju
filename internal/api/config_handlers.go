package api

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/user/kaiju/internal/agent"
	"github.com/user/kaiju/internal/config"
)

/*
 * ConfigAPI handles reading and updating the kaiju config from the UI.
 * desc: Provides GET/PATCH for runtime configuration and a model catalog endpoint.
 */
type ConfigAPI struct {
	cfg     *config.Config
	cfgPath string
	agent   *agent.Agent // for live-updating LLM client
}

/*
 * NewConfigAPI creates config API handlers.
 * desc: Constructs a ConfigAPI wired to the live config, its disk path, and the agent for live-reload.
 * param: cfg - pointer to the live configuration struct
 * param: cfgPath - filesystem path where the config JSON is persisted
 * param: ag - agent instance for live-updating LLM and executor clients
 * return: a configured ConfigAPI instance
 */
func NewConfigAPI(cfg *config.Config, cfgPath string, ag *agent.Agent) *ConfigAPI {
	return &ConfigAPI{cfg: cfg, cfgPath: cfgPath, agent: ag}
}

/*
 * RegisterRoutes mounts config routes on the mux.
 * desc: Registers get-config, update-config, and list-models endpoints.
 * param: mux - the HTTP serve mux to attach routes to
 */
func (c *ConfigAPI) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/config", c.handleGetConfig)
	mux.HandleFunc("PATCH /api/v1/config", c.handleUpdateConfig)
	mux.HandleFunc("GET /api/v1/models", c.handleListModels)
}

/*
 * handleGetConfig returns the current configuration with secrets masked.
 * desc: Copies the config, masks the API key and JWT secret, and returns it as JSON.
 * param: w - HTTP response writer
 */
func (c *ConfigAPI) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
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
	Agent *struct {
		DAGEnabled  *bool   `json:"dag_enabled,omitempty"`
		DAGMode     *string `json:"dag_mode,omitempty"`
		PlannerMode *string `json:"planner_mode,omitempty"`
		SafetyLevel *int    `json:"safety_level,omitempty"`
	} `json:"agent,omitempty"`
}

/*
 * handleUpdateConfig applies a partial config patch, live-updates the agent, and saves to disk.
 * desc: Merges non-nil fields from the patch into the live config, hot-reloads LLM/executor clients, and persists the result.
 * param: w - HTTP response writer
 * param: r - HTTP request containing a configPatch JSON body
 */
func (c *ConfigAPI) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
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
		if patch.Agent.PlannerMode != nil {
			c.cfg.Agent.PlannerMode = *patch.Agent.PlannerMode
		}
		if patch.Agent.SafetyLevel != nil {
			c.cfg.Agent.SafetyLevel = *patch.Agent.SafetyLevel
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

	// Live-update the agent's LLM clients when config changes
	if patch.LLM != nil && c.agent != nil {
		c.agent.SetLLMClient(c.cfg.LLM.Provider, c.cfg.LLM.Endpoint, c.cfg.LLM.APIKey, c.cfg.LLM.Model)
		log.Printf("[config] reasoning model updated: provider=%s model=%s", c.cfg.LLM.Provider, c.cfg.LLM.Model)
	}
	if patch.Executor != nil && c.agent != nil && c.cfg.Executor.Model != "" {
		ep := c.cfg.Executor.Endpoint
		if ep == "" { ep = c.cfg.LLM.Endpoint }
		prov := c.cfg.Executor.Provider
		if prov == "" { prov = c.cfg.LLM.Provider }
		key := c.cfg.Executor.APIKey
		if key == "" { key = c.cfg.LLM.APIKey }
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
func (c *ConfigAPI) saveToDisk() error {
	data, err := json.MarshalIndent(c.cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.cfgPath, data, 0600)
}

// ─── Model Catalog ──────────────────────────────────────────────────────────

/*
 * modelInfo describes a supported LLM model for the catalog.
 * desc: Contains the model ID, display name, provider, and optional context window size.
 */
type modelInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Context  string `json:"context,omitempty"`
}

/*
 * handleListModels returns the supported model catalog.
 * desc: Returns the static list of all known LLM models across providers.
 * param: w - HTTP response writer
 */
func (c *ConfigAPI) handleListModels(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, allModels, http.StatusOK)
}

// allModels is the supported model catalog.
var allModels = []modelInfo{
	// OpenAI
	{ID: "gpt-4o", Name: "GPT-4o", Provider: "openai", Context: "128K"},
	{ID: "gpt-4o-mini", Name: "GPT-4o Mini", Provider: "openai", Context: "128K"},
	{ID: "gpt-4.1", Name: "GPT-4.1", Provider: "openai", Context: "1M"},
	{ID: "gpt-4.1-mini", Name: "GPT-4.1 Mini", Provider: "openai", Context: "1M"},
	{ID: "gpt-4.1-nano", Name: "GPT-4.1 Nano", Provider: "openai", Context: "1M"},
	{ID: "o3", Name: "o3", Provider: "openai", Context: "200K"},
	{ID: "o3-mini", Name: "o3 Mini", Provider: "openai", Context: "200K"},
	{ID: "o4-mini", Name: "o4 Mini", Provider: "openai", Context: "200K"},
	{ID: "codex-mini", Name: "Codex Mini", Provider: "openai", Context: "1M"},

	// Anthropic
	{ID: "claude-sonnet-4-20250514", Name: "Claude Sonnet 4", Provider: "anthropic", Context: "200K"},
	{ID: "claude-opus-4-20250514", Name: "Claude Opus 4", Provider: "anthropic", Context: "200K"},
	{ID: "claude-haiku-4-20250414", Name: "Claude Haiku 4", Provider: "anthropic", Context: "200K"},

	// Google
	{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Provider: "google", Context: "1M"},
	{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash", Provider: "google", Context: "1M"},

	// Qwen
	{ID: "qwen/qwen3-235b-a22b", Name: "Qwen3 235B", Provider: "openrouter", Context: "128K"},
	{ID: "qwen/qwen3-30b-a3b", Name: "Qwen3 30B", Provider: "openrouter", Context: "128K"},
	{ID: "qwen/qwen3-32b", Name: "Qwen3 32B", Provider: "openrouter", Context: "128K"},
	{ID: "qwen/qwen3-14b", Name: "Qwen3 14B", Provider: "openrouter", Context: "128K"},
	{ID: "qwen/qwen3-8b", Name: "Qwen3 8B", Provider: "openrouter", Context: "128K"},
	{ID: "qwen/qwen3-4b", Name: "Qwen3 4B", Provider: "openrouter", Context: "128K"},
	{ID: "qwen/qwen3-1.7b", Name: "Qwen3 1.7B", Provider: "openrouter", Context: "128K"},
	{ID: "qwen/qwen3-0.6b", Name: "Qwen3 0.6B", Provider: "openrouter", Context: "128K"},
	{ID: "qwen/qwq-32b", Name: "QwQ 32B (Reasoning)", Provider: "openrouter", Context: "128K"},
	{ID: "qwen/qwen-2.5-coder-32b-instruct", Name: "Qwen 2.5 Coder 32B", Provider: "openrouter", Context: "128K"},

	// OpenRouter — other popular models
	{ID: "anthropic/claude-sonnet-4", Name: "Claude Sonnet 4", Provider: "openrouter", Context: "200K"},
	{ID: "anthropic/claude-opus-4", Name: "Claude Opus 4", Provider: "openrouter", Context: "200K"},
	{ID: "anthropic/claude-haiku-4", Name: "Claude Haiku 4", Provider: "openrouter", Context: "200K"},
	{ID: "openai/gpt-4o", Name: "GPT-4o", Provider: "openrouter", Context: "128K"},
	{ID: "openai/gpt-4.1", Name: "GPT-4.1", Provider: "openrouter", Context: "1M"},
	{ID: "openai/o3", Name: "o3", Provider: "openrouter", Context: "200K"},
	{ID: "openai/o4-mini", Name: "o4 Mini", Provider: "openrouter", Context: "200K"},
	{ID: "openai/codex-mini", Name: "Codex Mini", Provider: "openrouter", Context: "1M"},
	{ID: "google/gemini-2.5-pro", Name: "Gemini 2.5 Pro", Provider: "openrouter", Context: "1M"},
	{ID: "google/gemini-2.5-flash", Name: "Gemini 2.5 Flash", Provider: "openrouter", Context: "1M"},
	{ID: "meta-llama/llama-4-maverick", Name: "Llama 4 Maverick", Provider: "openrouter", Context: "1M"},
	{ID: "meta-llama/llama-4-scout", Name: "Llama 4 Scout", Provider: "openrouter", Context: "512K"},
	{ID: "deepseek/deepseek-r1", Name: "DeepSeek R1", Provider: "openrouter", Context: "64K"},
	{ID: "deepseek/deepseek-chat-v3-0324", Name: "DeepSeek V3", Provider: "openrouter", Context: "64K"},
	{ID: "deepseek/deepseek-r1-0528", Name: "DeepSeek R1 0528", Provider: "openrouter", Context: "64K"},
	{ID: "mistralai/mistral-large", Name: "Mistral Large", Provider: "openrouter", Context: "128K"},
	{ID: "mistralai/codestral", Name: "Codestral", Provider: "openrouter", Context: "256K"},
	{ID: "x-ai/grok-3", Name: "Grok 3", Provider: "openrouter", Context: "128K"},
	{ID: "x-ai/grok-3-mini", Name: "Grok 3 Mini", Provider: "openrouter", Context: "128K"},
	{ID: "cohere/command-a", Name: "Command A", Provider: "openrouter", Context: "256K"},

	// Local (Ollama)
	{ID: "llama3.1:8b", Name: "Llama 3.1 8B (local)", Provider: "ollama"},
	{ID: "qwen3:8b", Name: "Qwen3 8B (local)", Provider: "ollama"},
	{ID: "deepseek-r1:14b", Name: "DeepSeek R1 14B (local)", Provider: "ollama"},
	{ID: "codestral:latest", Name: "Codestral (local)", Provider: "ollama"},
}
