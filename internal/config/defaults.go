package config

/*
 * Default returns a Config with sensible defaults.
 * desc: Provides a fully populated Config using reasonable production defaults for all fields.
 * return: a pointer to the default Config
 */
func Default() *Config {
	return &Config{
		LLM: LLMConfig{
			Provider:    "openai",
			Endpoint:    "https://api.openai.com/v1",
			Model:       "gpt-4o",
			Temperature: 0.3,
			MaxTokens:   4096,
		},
		Agent: AgentConfig{
			DAGEnabled:       true,
			DAGMode:          "orchestrator",
			MaxNodes:         100,
			MaxPerSkill:      10,
			MaxLLMCalls:      20,
			MaxObserverCalls: 50,
			BatchSize:        5,
			WallClockSec:     180,
			MaxTurns:         15,
			RateLimit:        100,
			SafetyLevel:      1,
			PlannerMode:      "structured",
			DataDir:          "~/.kaiju",
			Workspace:        "", // defaults to ~/.kaiju/workspace (resolved in config.resolve)
		},
		Channels: ChannelsConfig{
			CLI: CLIChannelConfig{Enabled: true},
			Web: WebChannelConfig{Enabled: true, Port: 8080},
		},
		API: APIConfig{
			Enabled: false,
			Port:    8081,
		},
		Tools: ToolsConfig{
			Bash:    BashToolConfig{Enabled: true, Shell: "auto"},
			File:    FileToolConfig{Enabled: true, AllowedPaths: []string{"."}},
			Web:     WebToolConfig{Enabled: true},
			Sysinfo: SysinfoConfig{Enabled: true},
		},
		SkillsDirs: []string{"~/.kaiju/skills"},
	}
}
