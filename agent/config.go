package agent

import (
	"io/fs"
	"time"
)

// Configuration.
//
// Config is grouped rather than flat. It had grown to sixty fields with model
// credentials next to file paths next to execution limits, which told an
// application nothing about which of them it actually had to think about.
//
// The groups are embedded, so field access is unchanged: cfg.MaxNodes and
// cfg.DataDir still read exactly as before, and no code inside this package
// needed touching. Only construction changes, which is the one place the
// grouping is worth anything — a caller now fills in the two or three groups
// its situation calls for and can see at a glance what it has left alone.
//
// Everything has a working zero value. An application that supplies only a
// model and a data directory gets a functioning agent.

/*
 * Config holds agent configuration.
 * desc: Seven groups — models and credentials, paths, node identity, the DAG
 *       engine, skill routing, compute, and the capabilities an embedding
 *       application supplies.
 */
type Config struct {
	ModelConfig
	PathConfig
	IdentityConfig
	DAGConfig
	RoutingConfig
	ComputeConfig
	Capabilities
}

// ModelConfig is which models are called, and with what credentials.
//
// There are three levels. The main model is the fallback for everything. The
// executor is a cheaper lane for reflection, observation and micro-planning.
// The per-lane choices override a single stage. Each level falls back to the
// one above field by field, so naming only a cheaper model for a lane inherits
// the endpoint and key from the main model.
type ModelConfig struct {
	// LLMProvider names the provider for the main model ("openai",
	// "anthropic", "openrouter"). Empty defaults to openai.
	LLMProvider string
	LLMEndpoint string
	LLMAPIKey   string
	LLMModel    string

	// Providers is the credential catalog for per-request model routing,
	// keyed by provider name (openai, anthropic, openrouter, selfhosted, …).
	// Built into one llm.Client per provider at boot; a request selects a
	// provider+model and kaiju routes to the matching keyed client. Keys live
	// only here — requests carry a selection, never a key.
	Providers map[string]ProviderCreds

	Temperature float64
	MaxTokens   int
	RateLimit   int

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

	// Limits reports a model's published token limits, so a call can size its
	// reply cap against the model rather than against a constant. Nil switches
	// the mechanism off and every call keeps the cap it already had; the same
	// happens per model when the lookup returns zeroes, so a model missing from
	// the application's catalog is safe rather than broken.
	Limits ModelLimits
}

// ModelLimits reports what a model can take in and give back, in tokens. Zero
// for either means the caller does not know, not that the limit is zero.
type ModelLimits func(model string) (contextTokens, maxOutputTokens int)

// PathConfig is where the agent reads and writes.
type PathConfig struct {
	DataDir     string
	Workspace   string // where files are written (cwd in CLI mode, sandbox in web mode)
	MetadataDir string // where blueprints, worklog, sessions live (.kaiju/ in CLI, same as workspace in web)
	CLIMode     bool   // true = workspace is cwd, no project/ prefix, .kaiju/ for metadata

	// BuiltinSkills are skill cards the application compiled into its own
	// binary, laid out as <name>/SKILL.md at the root of the filesystem.
	//
	// The engine's own cards load first and these load over them, so a card
	// sharing a name with one of the engine's replaces it. Nil means the
	// application ships none, which is the ordinary case; cards on disk are a
	// separate matter and override both.
	BuiltinSkills fs.FS
}

// IdentityConfig is who this agent is and how much authority it starts with.
//
// "Node" here means a machine, not a step in a plan — the one place in this
// package where the word carries the other meaning. See O4 in the design notes.
type IdentityConfig struct {
	NodeID        string
	NodeRole      string // "node" or "coordinator"
	NodeClearance int    // IGX clearance (0 = default 1)
}

// DAGConfig governs the optimistic parallel execution engine: how large a plan
// may get, how many times it may reconsider, and when it is cut off.
type DAGConfig struct {
	DAGEnabled bool
	DAGMode    string // "reflect", "nReflect", "orchestrator" (default: "orchestrator")

	MaxTurns         int
	MaxNodes         int
	MaxPerSkill      int
	MaxLLMCalls      int
	MaxObserverCalls int // separate budget for observer LLM calls (default: 50)
	BatchSize        int // nodes completed before injecting reflection in nReflect mode (default: 5)

	MaxInvestigations int // max investigation cycles (Holmes + fix attempts) before forcing conclude (default: 1)
	MaxReplans        int // max EXPAND replan cycles (successful batch → executive plans next steps) before forcing conclude (default: 3)
	MaxHolmesIters    int // max ReAct iterations per Holmes investigation (default: 5)

	ExecutionMode               string // "interactive" (chat allowed) or "autonomous" (always investigate)
	DAGWallClock                time.Duration
	MaxConcurrentInvestigations int // scheduler worker-pool size; 0 => defaultConcurrency (1). Raise once per-principal fairness lands.
}

// RoutingConfig decides which skills a query reaches, and what the agent is
// told about itself before it starts.
type RoutingConfig struct {
	// Semantic skill routing.
	EmbeddingsEnabled bool
	EmbedEndpoint     string
	EmbedAPIKey       string
	EmbedModel        string
	EmbedTopK         int
	EmbedThreshold    float64
	AlwaysInclude     []string
	ClassifierEnabled bool // enable per-query skill card classification (extra LLM call)

	CustomSystemPrompt string
	BootMDPath         string
}

// ComputeConfig governs compute nodes — steps that run generated code rather
// than call a tool.
type ComputeConfig struct {
	ComputeTimeout time.Duration // max code execution time for compute nodes (default 120s)
	DisableCoding  bool          // when true, deep compute (architect/codebase building) is refused; shallow analytical compute still works
}

// Capabilities are what an embedding application supplies.
//
// Each is optional, and nil means the capability is absent rather than broken.
// An application that never sends work to another machine leaves Remote and
// ValidateTarget unset, and targets are then inert.
//
// These were a dozen Set* methods called after New, which meant a caller had
// to know they existed, which were required, and in what order. Anything
// genuinely settable while running stayed a method — see SetToolEnabled,
// SetClearance and SetDAGEnabled.
type Capabilities struct {
	// Unattended reports whether a run has nobody watching it, which decides
	// whether it may ask a question or use tools that record a person's
	// judgement. Nil uses Trigger.ExecutionMode.
	Unattended UnattendedFunc

	// TokenCategory names the usage bucket a run's spend is counted against.
	// Nil counts interactive callers as chat and everything else as background.
	TokenCategory TokenCategoryFunc

	// Admit decides whether a run may start at all — a licence, a maintenance
	// window, a quota, an operator pause. Nil admits everything. Distinct from
	// Clearance, which decides what a run may DO once it has started.
	Admit AdmitFunc

	// Refine adjusts what preflight concluded, using facts this package cannot
	// have — or replies with a question instead of planning, when the request
	// cannot be acted on as written. Nil leaves preflight's answer standing.
	Refine RefineFunc

	// Answer writes a finished run's final answer, for an application whose
	// result is a structured verdict rather than text for a person to read. Nil
	// leaves every answer to the built-in aggregator, and so does returning
	// nothing for a particular run.
	Answer AnswerFunc

	// AllowTool refuses a tool call for a reason this package cannot know — a
	// rule of the application's own, applied after the engine's own gate has
	// passed the call. Nil allows everything the gate allowed.
	AllowTool AllowToolFunc

	// Clearance decides how much authority a run has.
	Clearance ClearanceChecker

	// Store records completed runs and the actions taken during them. Nil
	// records nothing, and no behaviour depends on the writes succeeding.
	Store EventStore

	// Remote runs a step on the machine its Target names. Nil makes targets
	// inert — every step runs locally, as before targets existed.
	Remote RemoteExecutor

	// ValidateTarget rejects a malformed target before Remote is called. Nil
	// accepts any non-empty target.
	ValidateTarget TargetValidator

	// RunTargets reports which machines a run concerns, for reporting only —
	// nothing is dispatched from it. Nil falls back to the run's own Target.
	RunTargets TargetLister

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
	//	cfg.Capabilities.DescribeTrigger = func(t Trigger) string {
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
