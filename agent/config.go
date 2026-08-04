package agent

import "time"

/*
 * Config holds agent configuration.
 * desc: Contains LLM endpoint settings, rate limits, IGX clearance,
 *       DAG engine parameters, embedding configuration, and paths.
 */
type Config struct {
	LLMEndpoint string
	LLMAPIKey   string
	LLMModel    string
	// Providers is the credential catalog for per-request model routing,
	// keyed by provider name (openai, anthropic, openrouter, selfhosted, …).
	// Built into one llm.Client per provider at boot; a request selects a
	// provider+model and kaiju routes to the matching keyed client. Keys live
	// only here — requests carry a selection, never a key.
	Providers     map[string]ProviderCreds
	MaxTurns      int
	Temperature   float64
	MaxTokens     int
	RateLimit     int
	NodeClearance int    // IGX clearance (0 = default 1)
	NodeRole      string // "node" or "coordinator"
	DataDir       string
	Workspace     string // where files are written (cwd in CLI mode, sandbox in web mode)
	MetadataDir   string // where blueprints, worklog, sessions live (.kaiju/ in CLI, same as workspace in web)
	CLIMode       bool   // true = workspace is cwd, no project/ prefix, .kaiju/ for metadata
	NodeID        string

	// DAG engine (optimistic parallel investigation)
	DAGEnabled                  bool
	DAGMode                     string // "reflect", "nReflect", "orchestrator" (default: "orchestrator")
	MaxNodes                    int
	MaxPerSkill                 int
	MaxLLMCalls                 int
	MaxObserverCalls            int    // separate budget for observer LLM calls (default: 50)
	BatchSize                   int    // nodes completed before injecting reflection in nReflect mode (default: 5)
	MaxInvestigations           int    // max investigation cycles (Holmes + fix attempts) before forcing conclude (default: 1)
	MaxReplans                  int    // max EXPAND replan cycles (successful batch → executive plans next steps) before forcing conclude (default: 3)
	MaxHolmesIters              int    // max ReAct iterations per Holmes investigation (default: 5)
	ExecutionMode               string // "interactive" (chat allowed) or "autonomous" (always investigate)
	DAGWallClock                time.Duration
	MaxConcurrentInvestigations int // scheduler worker-pool size; 0 => defaultConcurrency (1). Raise once per-principal fairness lands.

	// Embeddings (semantic skill routing)
	EmbeddingsEnabled  bool
	EmbedEndpoint      string
	EmbedAPIKey        string
	EmbedModel         string
	EmbedTopK          int
	EmbedThreshold     float64
	AlwaysInclude      []string
	CustomSystemPrompt string
	BootMDPath         string
	ClassifierEnabled  bool // enable per-query capability card classification (extra LLM call)

	// Compute node
	ComputeTimeout time.Duration // max code execution time for compute nodes (default 120s)
	DisableCoding  bool          // when true, deep compute (architect/codebase building) is refused; shallow analytical compute still works

	// ─── Capabilities ────────────────────────────────────────────────────
	//
	// Supplied by the embedding application and applied by New. Each is
	// optional: nil means the capability is absent, not broken. An application
	// that never sends work to another machine leaves Remote and Validate
	// unset, and targets are then inert.
	//
	// These were a dozen Set* methods called after New, which meant a caller
	// had to know they existed, which were required, and in what order.
	// Anything genuinely settable while running stayed a method — see
	// SetToolEnabled, SetClearance and SetDAGEnabled.

	// LLMProvider names the provider for the main model ("openai",
	// "anthropic", "openrouter"). Empty defaults to openai.
	LLMProvider string

	// The executor is the cheaper lane used for reflection, observation and
	// micro-planning. Empty fields fall back to the main model, so it can be
	// configured partially or not at all.
	ExecutorProvider string
	ExecutorEndpoint string
	ExecutorAPIKey   string
	ExecutorModel    string

	// Per-lane model choices. Empty leaves that lane on the main model.
	VisionProvider, VisionModel string
	ChatProvider, ChatModel     string
	RouteProvider, RouteModel   string
	AnswerProvider, AnswerModel string

	// ChatTools limits which tools the chat lane may use. Empty means all.
	ChatTools []string

	// Clearance decides how much authority a run has.
	Clearance ClearanceChecker

	// Store records completed runs and the actions taken during them. Nil
	// records nothing, and no behaviour depends on the writes succeeding.
	Store EventStore

	// Remote runs a step on the machine its Target names. Nil makes targets
	// inert — every step runs locally, as before targets existed.
	Remote RemoteExecutor

	// Validate rejects a malformed target before Remote is called. Nil accepts
	// any non-empty target.
	Validate TargetValidator

	// Environment describes the surroundings a run happens in — which machines
	// exist, what is reachable, whatever the application decides the planner
	// should know. Appended verbatim to planning and reflection prompts. Nil,
	// or an empty string, adds nothing.
	//
	// A function rather than an interface: one method, no state, and an
	// interface would be ceremony. This package attaches no meaning to the
	// text — how it is worded is the application's business.
	Environment func() string

	// DescribeTrigger renders a run's starting point as the text the planner
	// reads. It is how an application explains its own kinds of work — an
	// alert, a sensor reading, a ticket — without this package knowing what
	// any of them are.
	//
	// Returning "" falls through to the built-in rendering, so an application
	// handles only what it recognises and leaves the rest alone:
	//
	//	cfg.DescribeTrigger = func(t Trigger) string {
	//		c, ok := t.Cause.(*MyCause)
	//		if !ok {
	//			return ""          // not mine — use the default
	//		}
	//		return render(c)
	//	}
	//
	// Nil behaves the same as returning "" for everything.
	DescribeTrigger func(Trigger) string
}
