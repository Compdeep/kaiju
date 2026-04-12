package config

/*
 * Default returns a Config with sensible defaults.
 * desc: Provides a fully populated Config using reasonable production defaults for all fields.
 * return: a pointer to the default Config
 */
func Default() *Config {
	classifierOn := true
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
			MaxInvestigations: 1,
			ExecutionMode:    "interactive",
			WallClockSec:     180,
			MaxTurns:         15,
			RateLimit:        100,
			SafetyLevel:      100,
			ExecutiveMode:       "structured",
			DataDir:           "~/.kaiju",
			Workspace:         "", // defaults to ~/.kaiju/workspace (resolved in config.resolve)
			ClassifierEnabled: &classifierOn,
			// Default intent ladder. Admins may replace this entirely via
			// kaiju.json or the admin UI. Go code only ever sees ranks —
			// names are purely presentation/config data.
			Intents: []IntentSeed{
				{
					Name:              "observe",
					Rank:              0,
					Description:       "Read-only — inspect data and state without making changes",
					PromptDescription: "Read-only actions that inspect data or state. Look up, analyze, check status, list files, read configs. No side effects, nothing created or modified.",
					Builtin:           true,
				},
				{
					Name:              "operate",
					Rank:              100,
					Description:       "Normal work — reversible side effects",
					PromptDescription: "Actions with reversible side effects. Write files, modify state, create resources, install dependencies, run code, start services, configure settings. The default working level for real tasks.",
					Builtin:           true,
					Default:           true,
				},
				{
					Name:              "override",
					Rank:              200,
					Description:       "Destructive — irreversible actions",
					PromptDescription: "Destructive or irreversible actions. Delete, remove, drop, kill, purge, force, wipe, uninstall. Requires explicit elevation.",
					Builtin:           true,
				},
			},
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
			Compute: ComputeToolConfig{Enabled: true, TimeoutSec: 120},
		},
		SkillsDirs: []string{"~/.kaiju/skills"},
	}
}
