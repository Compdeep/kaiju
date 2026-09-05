package config

import "github.com/Compdeep/kaiju/ui"

/*
 * Default returns a Config with sensible defaults.
 * desc: Provides a fully populated Config using reasonable production defaults for all fields.
 * return: a pointer to the default Config
 */
func Default() *Config {
	classifierOn := true
	return &Config{
		// Every section on: this is kaiju's own interface, and it is the whole
		// one. An application embedding the interface gets the opposite default
		// — see the ui package — because the question there is what happens when
		// somebody forgets. Here nobody can: this line is the answer.
		UI: ui.Config{Sections: ui.AllSections()},
		// Open-weight by default, on every lane that has a sensible open choice.
		// A deployment that cannot send its data to a vendor should not have to
		// change anything to be safe, and one that wants a closed model is
		// making a deliberate choice rather than accepting a default.
		//
		// The reasoning lane: qwen3-235b-a22b-2507, a 235B mixture-of-experts
		// with 22B active, so it is large without being slow, and it has no
		// reasoning phase to pay for. Open weights, ten providers on OpenRouter
		// and downloadable, so the same default works on-premise.
		LLM: LLMConfig{
			Provider:    "openrouter",
			Endpoint:    "https://openrouter.ai/api/v1",
			Model:       "qwen/qwen3-235b-a22b-2507",
			Temperature: 0.3,
			MaxTokens:   4096,
		},
		// The executor: qwen3-30b-a3b-instruct-2507, 3B active per token and the
		// cheapest thing measured that answers a forced call — 283 to 413 tokens
		// on the real preflight schema. This lane runs a dozen times per
		// investigation where the reasoning lane runs once, so its cost and
		// latency are what a run is actually made of.
		Executor: ExecutorConfig{
			Provider: "openrouter",
			Model:    "qwen/qwen3.6-35b-a3b",
		},
		Chat: ChatConfig{
			// Tools below is the palette an escalated agent may use, so a
			// tool-capable model is preferred. The same open-weight instruct
			// model the executor uses: cheap, tool-capable, and one model fewer
			// for a deployment to obtain. Leaving it empty falls back to the
			// reasoning lane.
			Provider: "openrouter",
			Model:    "qwen/qwen3-30b-a3b-instruct-2507",
			// web_fetch by default: the palette available to an escalated agent. A
			// request that sends its own chat_tools overrides this.
			Tools: []string{"web_fetch"},
		},
		Agent: AgentConfig{
			DAGEnabled:        true,
			DAGMode:           "orchestrator",
			MaxNodes:          100,
			MaxPerSkill:       10,
			MaxLLMCalls:       20,
			MaxObserverCalls:  50,
			BatchSize:         5,
			MaxInvestigations: 5,
			MaxReplans:        3,
			MaxConcurrent:     3,
			ExecutionMode:     "interactive",
			// Default the routing decision to a small, NON-reasoning tool-caller so
			// "does this need the agent?" is reliable within the router's 16-token
			// budget. gpt-4.1-mini benched 100% route-acc / 100% budget-fit / ~700ms
			// (a reasoning model like gpt-5-mini starves at 16 tokens → silent chat
			// fallback). See docs/router-model-bench.md. Overridable everywhere.
			RouteProvider:     "openrouter",
			RouteModel:        "qwen/qwen3.6-35b-a3b",
			WallClockSec:      180,
			MaxTurns:          15,
			RateLimit:         100,
			SafetyLevel:       100,
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
