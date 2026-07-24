package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

/*
 * Config holds the full kaiju configuration.
 * desc: Top-level struct aggregating LLM, executor, agent, channels, API, tools, and skills directory settings.
 */
type Config struct {
	LLM      LLMConfig      `json:"llm"`
	Executor ExecutorConfig `json:"executor"`
	// Vision is the model that answers questions about attached images directly
	// (bypassing the planner/tools) — so a tool-less vision model works, and
	// image attachments always route to a capable model regardless of the
	// reasoning model. Empty ⇒ no dedicated lane (images fall back to the
	// reasoning model if it supports vision).
	Vision VisionConfig `json:"vision,omitempty"`
	// Chat is a direct-completion lane (no planner/tools) for plain conversation
	// and non-tool-calling models (roleplay fine-tunes). Empty ⇒ reasoning model.
	Chat ChatConfig `json:"chat,omitempty"`
	// Providers is the credential catalog for per-request model routing. Each
	// entry is a named provider (openai, anthropic, openrouter, selfhosted, …)
	// holding the endpoint + key. The host (e.g. makeen) selects a provider +
	// model per request; kaiju resolves the name to the keyed client here. The
	// KEYS live only here — callers never supply a key, only a selection.
	Providers  map[string]ProviderConfig `json:"providers,omitempty"`
	Agent      AgentConfig               `json:"agent"`
	Channels   ChannelsConfig            `json:"channels"`
	API        APIConfig                 `json:"api"`
	Tools      ToolsConfig               `json:"tools"`
	SkillsDirs []string                  `json:"skills_dirs"`
	// Plugins names the optional, build-tag-gated plugins to switch on at startup
	// (e.g. ["pdf"]). A name here only takes effect if the binary was compiled
	// with that plugin's tag (`-tags plugin_pdf`); otherwise it's reported as
	// missing and ignored. See internal/plugins.
	Plugins []string `json:"plugins,omitempty"`
}

/*
 * ProviderConfig holds the credentials for one routable provider.
 * desc: Endpoint + key for a named provider. Type is the wire protocol
 *       ("openai" default, or "anthropic"); it defaults to the map key when
 *       empty, so "selfhosted" (an OpenAI-compatible endpoint) should set
 *       Type:"openai" while pointing Endpoint at the private host.
 */
type ProviderConfig struct {
	Type     string `json:"type,omitempty"` // wire protocol: "openai" (default) | "anthropic"
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"api_key"`
}

/*
 * LLMConfig configures the reasoning model (planner, aggregator, classifier).
 * desc: Holds provider, endpoint, API key, model name, temperature, and max tokens for the primary LLM.
 */
type LLMConfig struct {
	Provider    string  `json:"provider"`
	Endpoint    string  `json:"endpoint"`
	APIKey      string  `json:"api_key"`
	Model       string  `json:"model"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`
}

/*
 * ExecutorConfig configures the executor model (reflection, observer, micro-planner, compactor).
 * desc: Optional secondary model config; falls back to the reasoning model if empty.
 */
type ExecutorConfig struct {
	Provider string `json:"provider,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	APIKey   string `json:"api_key,omitempty"` // defaults to LLM.APIKey if empty
	Model    string `json:"model,omitempty"`
}

/*
 * VisionConfig configures the vision lane — the model that answers questions
 * about attached images via a direct completion. Provider is a name from the
 * providers block; the key stays there.
 */
type VisionConfig struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

/*
 * ChatConfig configures the chat lane — a direct completion with NO planner/DAG/
 * tools, for plain conversation and models that can't tool-call (roleplay
 * fine-tunes, etc.). Empty ⇒ same as the reasoning model.
 */
type ChatConfig struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// Tools is the default chat-lane tool allowlist, used when a request sends no
	// chat_tools of its own. Empty ⇒ pure chat. Include "agent" to let chat
	// delegate deep, multi-step work to the full executive.
	Tools []string `json:"tools,omitempty"`
}

/*
 * AgentConfig configures the agent's DAG execution parameters and resource limits.
 * desc: Controls DAG mode, node/call limits, rate limiting, safety level, data directory, and workspace path.
 */
type AgentConfig struct {
	DAGEnabled        bool        `json:"dag_enabled"`
	DAGMode           string      `json:"dag_mode"`
	MaxNodes          int         `json:"max_nodes"`
	MaxPerSkill       int         `json:"max_per_skill"`
	MaxLLMCalls       int         `json:"max_llm_calls"`
	MaxObserverCalls  int         `json:"max_observer_calls"`
	BatchSize         int         `json:"batch_size"`
	MaxInvestigations int         `json:"max_investigations"`
	MaxReplans        int         `json:"max_replans"`
	MaxConcurrent     int         `json:"max_concurrent"` // scheduler worker-pool size (concurrent investigations); 0 => default (3)
	DisableCoding     bool        `json:"disable_coding"` // true = refuse deep compute (codebase building); enterprise deployments set this
	ExecutionMode     string      `json:"execution_mode"` // "interactive" (default) or "autonomous"
	// RouteProvider/RouteModel pin the model for the cheap chat-vs-investigate
	// routing decision (preflight). Empty ⇒ the executor lane. A small capable
	// model here makes the run-the-agent decision reliable without making every
	// background call pricier. Overridable via config, the config API, or the CLI.
	RouteProvider     string      `json:"route_provider,omitempty"`
	RouteModel        string      `json:"route_model,omitempty"`
	WallClockSec      int         `json:"wall_clock_sec"`
	MaxTurns          int         `json:"max_turns"`
	RateLimit         int         `json:"rate_limit"`
	SafetyLevel       int         `json:"safety_level"`
	DataDir           string      `json:"data_dir"`
	Workspace         string      `json:"workspace"`
	MetadataDir       string      `json:"-"` // set at runtime — .kaiju/ in CLI, same as workspace in web
	CLIMode           bool        `json:"-"` // set at runtime, not from config file
	// ClassifierEnabled controls the pre-plan preflight LLM call that selects
	// skill guidance, infers intent, routes chat/meta queries, and hints
	// required tool categories. Default true — disabling degrades behavior
	// (no skill guidance, no chat short-circuit) and is only useful for tests.
	ClassifierEnabled *bool         `json:"classifier_enabled,omitempty"`
	// Intents optionally seeds the intent registry on first run. After the
	// DB has any intents rows, this is ignored — the DB is authoritative.
	// Admins can edit via the UI after first startup.
	Intents    []IntentSeed `json:"intents,omitempty"`
	Embeddings EmbedConfig  `json:"embeddings"`
}

/*
 * IntentSeed is a single intent definition in the config file.
 * desc: Used to bootstrap the intents table on first run. The config file
 *       is the sole source of truth for intent names and ranks — Go code
 *       never hardcodes specific intent names. Defaults live in Default()
 *       and can be fully replaced by user config.
 */
type IntentSeed struct {
	Name              string `json:"name"`
	Rank              int    `json:"rank"`
	Description       string `json:"description"`
	PromptDescription string `json:"prompt_description"`
	Builtin           bool   `json:"builtin,omitempty"`
	Default           bool   `json:"default,omitempty"` // exactly one intent should be marked default
}

/*
 * EmbedConfig configures the embedding model for semantic memory search.
 * desc: Controls whether embeddings are enabled, the endpoint/key/model to use, and similarity search parameters.
 */
type EmbedConfig struct {
	Enabled   bool    `json:"enabled"`
	Endpoint  string  `json:"endpoint"`
	APIKey    string  `json:"api_key"`
	Model     string  `json:"model"`
	TopK      int     `json:"top_k"`
	Threshold float64 `json:"threshold"`
}

/*
 * ChannelsConfig groups configuration for all communication channels.
 * desc: Contains sub-configs for CLI, web, Telegram, and Discord channels.
 */
type ChannelsConfig struct {
	CLI      CLIChannelConfig      `json:"cli"`
	Web      WebChannelConfig      `json:"web"`
	Telegram TelegramChannelConfig `json:"telegram"`
	Discord  DiscordChannelConfig  `json:"discord"`
}

/*
 * CLIChannelConfig configures the CLI channel.
 * desc: Controls whether the interactive CLI channel is enabled.
 */
type CLIChannelConfig struct {
	Enabled bool `json:"enabled"`
}

/*
 * WebChannelConfig configures the web UI channel.
 * desc: Controls whether the web channel is enabled and which port it listens on.
 */
type WebChannelConfig struct {
	Enabled bool `json:"enabled"`
	Port    int  `json:"port"`
}

/*
 * TelegramChannelConfig configures the Telegram bot channel.
 * desc: Controls whether the Telegram channel is enabled and holds the bot token.
 */
type TelegramChannelConfig struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token"`
}

/*
 * DiscordChannelConfig configures the Discord bot channel.
 * desc: Controls whether the Discord channel is enabled and holds the bot token.
 */
type DiscordChannelConfig struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token"`
}

/*
 * APIConfig configures the REST API server.
 * desc: Controls whether the API is enabled, its port, and authentication credentials.
 */
type APIConfig struct {
	Enabled   bool   `json:"enabled"`
	Port      int    `json:"port"`
	AuthToken string `json:"auth_token"`  // legacy bearer token (backward compat)
	JWTSecret string `json:"jwt_secret"`  // auto-generated if empty
}

/*
 * ToolsConfig groups configuration for built-in tool categories.
 * desc: Contains sub-configs for bash, file, web, and sysinfo tools.
 */
type ToolsConfig struct {
	Bash    BashToolConfig    `json:"bash"`
	File    FileToolConfig    `json:"file"`
	Web     WebToolConfig     `json:"web"`
	Sysinfo SysinfoConfig    `json:"sysinfo"`
	Compute ComputeToolConfig `json:"compute"`
}

/*
 * ComputeToolConfig configures the LLM-powered compute tool.
 * desc: Controls whether compute nodes are enabled and the max code execution timeout.
 */
type ComputeToolConfig struct {
	Enabled    bool `json:"enabled"`
	TimeoutSec int  `json:"timeout_sec"` // max code execution time (default 120)
}

/*
 * BashToolConfig configures the bash/shell execution tool.
 * desc: Controls whether bash is enabled and which shell binary to use.
 */
type BashToolConfig struct {
	Enabled bool   `json:"enabled"`
	Shell   string `json:"shell"`
}

/*
 * FileToolConfig configures the file read/write tool.
 * desc: Controls whether file operations are enabled and restricts access to allowed paths.
 */
type FileToolConfig struct {
	Enabled      bool     `json:"enabled"`
	AllowedPaths []string `json:"allowed_paths"`
}

/*
 * WebToolConfig configures the web search/fetch tool.
 * desc: Controls whether web tools are enabled.
 */
type WebToolConfig struct {
	Enabled        bool    `json:"enabled"`
	SearchProvider string  `json:"search_provider"` // "startpage" (default), "ddg", "startpage+ddg"
	SearchDelaySec float64 `json:"search_delay_sec"` // min seconds between search requests (default 1.5)
}

/*
 * SysinfoConfig configures the system information tool.
 * desc: Controls whether the sysinfo tool is enabled.
 */
type SysinfoConfig struct {
	Enabled bool `json:"enabled"`
}

/*
 * Load reads a JSON config file from disk.
 * desc: Parses the JSON file at the given path, applies defaults for missing fields, and resolves environment variables and paths.
 * param: path - filesystem path to the JSON config file (supports ~ expansion)
 * return: the parsed and resolved Config, or an error if reading or parsing fails
 */
func Load(path string) (*Config, error) {
	path = expandPath(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	cfg.resolve()
	return cfg, nil
}

/*
 * resolve expands paths and env vars after loading.
 * desc: Processes environment variable substitution and tilde expansion on all relevant config fields.
 */
func (c *Config) resolve() {
	c.Agent.DataDir = expandPath(resolveEnv(c.Agent.DataDir))
	// Workspace defaults to <DataDir>/workspace if not set
	if c.Agent.Workspace == "" {
		c.Agent.Workspace = c.Agent.DataDir + "/workspace"
	} else {
		c.Agent.Workspace = expandPath(resolveEnv(c.Agent.Workspace))
	}
	c.LLM.APIKey = resolveEnv(c.LLM.APIKey)
	c.LLM.Endpoint = resolveEnv(c.LLM.Endpoint)
	c.Executor.APIKey = resolveEnv(c.Executor.APIKey)
	c.Executor.Endpoint = resolveEnv(c.Executor.Endpoint)
	for name, p := range c.Providers {
		p.APIKey = resolveEnv(p.APIKey)
		p.Endpoint = resolveEnv(p.Endpoint)
		c.Providers[name] = p
	}
	c.Channels.Telegram.Token = resolveEnv(c.Channels.Telegram.Token)
	c.Channels.Discord.Token = resolveEnv(c.Channels.Discord.Token)
	c.API.AuthToken = resolveEnv(c.API.AuthToken)
	for i, d := range c.SkillsDirs {
		c.SkillsDirs[i] = expandPath(resolveEnv(d))
	}

	// Auto-detect shell if set to "auto"
	if c.Tools.Bash.Shell == "auto" || c.Tools.Bash.Shell == "" {
		if runtime.GOOS == "windows" {
			c.Tools.Bash.Shell = "powershell"
		} else {
			c.Tools.Bash.Shell = "sh"
		}
	}
}

/*
 * resolveEnv replaces ${VAR} patterns with environment variable values.
 * desc: Uses os.Expand to substitute environment variable references in the string.
 * param: s - input string potentially containing ${VAR} patterns
 * return: the string with all environment variables expanded
 */
func resolveEnv(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return os.Expand(s, os.Getenv)
}

/*
 * expandPath expands ~ to the user's home directory.
 * desc: Replaces a leading tilde with the current user's home directory path.
 * param: p - filesystem path potentially starting with ~
 * return: the expanded path
 */
func expandPath(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}
