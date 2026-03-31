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
	Agent    AgentConfig    `json:"agent"`
	Channels ChannelsConfig `json:"channels"`
	API        APIConfig      `json:"api"`
	Tools      ToolsConfig    `json:"tools"`
	SkillsDirs []string       `json:"skills_dirs"`
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
 * AgentConfig configures the agent's DAG execution parameters and resource limits.
 * desc: Controls DAG mode, node/call limits, rate limiting, safety level, data directory, and workspace path.
 */
type AgentConfig struct {
	DAGEnabled       bool          `json:"dag_enabled"`
	DAGMode          string        `json:"dag_mode"`
	MaxNodes         int           `json:"max_nodes"`
	MaxPerSkill      int           `json:"max_per_skill"`
	MaxLLMCalls      int           `json:"max_llm_calls"`
	MaxObserverCalls int           `json:"max_observer_calls"`
	BatchSize        int           `json:"batch_size"`
	WallClockSec     int           `json:"wall_clock_sec"`
	MaxTurns         int           `json:"max_turns"`
	RateLimit        int           `json:"rate_limit"`
	SafetyLevel      int           `json:"safety_level"`
	DataDir          string        `json:"data_dir"`
	Workspace        string        `json:"workspace"`
	PlannerMode      string        `json:"planner_mode"`
	Embeddings       EmbedConfig   `json:"embeddings"`
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
	Bash    BashToolConfig `json:"bash"`
	File    FileToolConfig `json:"file"`
	Web     WebToolConfig  `json:"web"`
	Sysinfo SysinfoConfig `json:"sysinfo"`
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
