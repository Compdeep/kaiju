package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Compdeep/kaiju/agent/gates"
	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/prompt"
	"github.com/Compdeep/kaiju/agent/skillmd"
	"github.com/Compdeep/kaiju/agent/toolapi"
)

/*
 * ResolvedScope defines the effective tool permissions for a request.
 * desc: nil means full access (local CLI user, backward-compatible).
 *       Contains username, allowed tools set, per-tool impact caps,
 *       and maximum intent level.
 */
type ResolvedScope struct {
	Username     string          `json:"username,omitempty"`
	AllowedTools map[string]bool `json:"allowed_tools,omitempty"`
	MaxImpact    map[string]int  `json:"max_impact,omitempty"`
	MaxIntent    int             `json:"max_intent,omitempty"`
}

/*
 * Trigger describes what initiated an investigation.
 * desc: Contains the trigger type, the caller's own id, the raw data payload,
 *       DAG mode override, data directory override, IGX intent cap, tool
 *       access scope, session ID, and conversation history.
 */
type Trigger struct {
	// Target names the machine this run is about, if any. Steps calling tools
	// that require a target inherit it; see applyRunTarget. What goes in here,
	// and how it is chosen, is the caller's business.
	//
	// Named Target, not TargetNode: "node" already means a DAG node in this
	// package, and a machine is not one.
	Target string `json:"target,omitempty"`

	Type          string          `json:"type"` // "chat_query", "api_query", "scheduled", "event", "command"
	ID            string          `json:"id"`
	Data          json.RawMessage `json:"data"`
	Source        string          `json:"source"`                   // peer ID or "local"
	DAGMode       string          `json:"dag_mode"`                 // optional override: "reflect", "nReflect", "orchestrator"
	DataDir       string          `json:"data_dir"`                 // override data dir for retrieval skills (relay/gateway use temp path)
	MaxIntent     *int            `json:"max_intent,omitempty"`     // optional IGX cap (can only lower intent, never escalate)
	Scope         *ResolvedScope  `json:"scope,omitempty"`          // tool access scope (nil = full access)
	SessionID     string          `json:"session_id,omitempty"`     // conversation session for memory
	History       []llm.Message   `json:"history,omitempty"`        // conversation history
	AggMode       int             `json:"agg_mode,omitempty"`       // 0=skip aggregator, 1=executor model (default), 2=reasoning model
	ExecutionMode string          `json:"execution_mode,omitempty"` // per-request override: "interactive" or "autonomous"
	// Per-request model routing (all optional; empty ⇒ configured default).
	// Provider is a name in cfg.Providers; Model is that provider's model id.
	// Heavy lane = executive/aggregator/reasoning; Light lane = the executor
	// (classify/route/reflect/observe). Keys are never carried here.
	Provider           string `json:"provider,omitempty"`
	Model              string `json:"model,omitempty"`
	ExecutorProvider   string `json:"executor_provider,omitempty"`
	ExecutorModel      string `json:"executor_model,omitempty"`
	AnswerProvider     string `json:"answer_provider,omitempty"`
	AnswerModel        string `json:"answer_model,omitempty"`
	HeartbeatThreshold int    `json:"heartbeat_threshold,omitempty"` // consecutive stuck ticks before kernel interjects (0 = default 3; raise for long-running work like downloads)

	// Cause carries whatever the application knows about what prompted this
	// run — a monitoring event, a sensor reading, a support ticket. It is
	// opaque here: carried, handed back, never interpreted.
	//
	// The only thing that reads it is the application's own DescribeTrigger,
	// which turns it into the text the planner sees. That keeps the vocabulary
	// of one product out of this package while still letting the planner be
	// told what it is looking at.
	//
	// Deliberately not serialised: a Trigger is passed in-process, and typing
	// this as `any` costs nothing because nothing marshals the struct whole.
	Cause any `json:"-"`
}

/*
 * BuildMessagesWithHistory constructs a message array with optional history injection.
 * desc: Pattern: [system, ...history, user_query]. Used by executive, aggregator,
 *       and ReAct loop to build LLM message sequences.
 * param: system - the system prompt string.
 * param: userQuery - the user's query or trigger text.
 * param: history - conversation history messages to insert.
 * return: ordered slice of LLM messages.
 */
func BuildMessagesWithHistory(system, userQuery string, history []llm.Message) []llm.Message {
	msgs := make([]llm.Message, 0, 2+len(history))
	msgs = append(msgs, llm.Message{Role: "system", Content: system})
	msgs = append(msgs, history...)
	msgs = append(msgs, llm.Message{Role: "user", Content: userQuery})
	return msgs
}

/*
 * Intent returns the effective IGX intent for this trigger.
 * desc: For chat queries with no explicit override, returns IntentAuto so the
 *       executive can infer the appropriate level. All other trigger types return
 *       their structural intent (never auto — autonomous runs are hardcoded).
 *       MaxIntent can only LOWER intent for non-chat triggers (defense in depth).
 * return: the resolved IGX Intent value.
 */
func (t Trigger) Intent() gates.Intent {
	// Chat queries default to auto-inference unless the caller pinned a rank.
	if t.Type == "chat_query" {
		if t.MaxIntent != nil {
			return gates.Intent(*t.MaxIntent)
		}
		return gates.IntentAuto
	}
	// Non-chat triggers must carry an explicit rank set by the trigger
	// creator. Go has no knowledge of which rank is appropriate for which
	// trigger type — that policy lives in the caller/config.
	if t.MaxIntent != nil {
		return gates.Intent(*t.MaxIntent)
	}
	return gates.IntentAuto
}

/*
 * localClearance implements gates.ClearanceSource with a mutex-protected int.
 * desc: Thread-safe wrapper around the node's IGX clearance level.
 */
type localClearance struct {
	mu    sync.RWMutex
	value int
}

/*
 * Clearance returns the current clearance level.
 * desc: Thread-safe read of the clearance value.
 * return: the current clearance integer.
 */
func (lc *localClearance) Clearance() int {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.value
}

/*
 * Set updates the clearance level.
 * desc: Thread-safe write of the clearance value.
 * param: v - the new clearance level.
 */
func (lc *localClearance) Set(v int) {
	lc.mu.Lock()
	lc.value = v
	lc.mu.Unlock()
}

/*
 * ClearanceChecker validates tool execution against external authorization endpoints.
 * desc: Returns nil if no endpoint configured (default allow) or if authorized.
 *       Returns error if denied, unreachable, or timed out.
 */
type ClearanceChecker interface {
	Check(ctx context.Context, toolName string, params map[string]any, user string) error
}

/*
 * Agent is the agentic reasoning engine.
 * desc: Core agent struct that orchestrates investigations via DAG or ReAct loop.
 *       Manages LLM clients, tool registry, IGX gate, memory, gossip, IPC,
 *       embeddings, skill watching, the environment description, and live
 *       DAG streaming.
 */
type Agent struct {
	admitRun        AdmitFunc         // nil = every run is admitted
	isUnattended    UnattendedFunc    // nil = ExecutionMode decides
	tokenCategoryFn TokenCategoryFunc // nil = the built-in lane split
	refine          RefineFunc        // nil = preflight's own answer stands
	answer          AnswerFunc        // nil = the built-in aggregator writes every answer

	// classifyStub stands in for the two classification calls, so a test can
	// drive the pipeline around them — the routing, the autonomous override, the
	// short-circuit, the refinement — without a model.
	//
	// Unexported and set only by tests. It is not a way for an application to
	// replace classification: nothing has needed that, and an extension point
	// with no user is one more thing to keep working. The pipeline had no tests
	// at all before this existed.
	classifyStub func(query string, history []llm.Message) *PreflightResult
	allowToolFn  AllowToolFunc   // nil = every call the gate allowed proceeds
	remoteExec   RemoteExecutor  // nil = steps naming a machine run locally
	targetValid  TargetValidator // nil = any non-empty target accepted
	targetLister TargetLister    // nil = a run concerns only its own Target

	cfg      Config
	llm      *llm.Client // reasoning model (executive, aggregator, classifier)
	executor *llm.Client // executor model (reflection, observer, micro-planner)
	// providerClients holds one client per configured provider for per-request
	// model routing (see model_route.go). Nil/empty ⇒ routing off, everything
	// uses llm/executor as today.
	providerClients map[string]*llm.Client
	// Vision lane default — the model that answers image questions directly
	// (see api handleExecute). Provider is a name from providerClients.
	visionProvider string
	visionModel    string
	// Chat lane default — direct completion, no planner/tools. Empty ⇒ reasoning.
	chatProvider  string
	chatModel     string
	chatTools     []string
	routeProvider string
	routeModel    string
	// Answer lane default — the model that writes the FINAL answer (aggregator +
	// chat). Empty ⇒ the heavy/reasoning lane. Open-ended generation, so a thinking
	// model is fine here (unlike the planner/executor/router tool-call lanes).
	answerProvider    string
	answerModel       string
	registry          *toolapi.Registry
	gate              *gates.Gate
	clearanceCheck    ClearanceChecker // external authorization (nil = no check)
	clearance         *localClearance  // IGX node clearance
	clearanceExplicit bool             // true if cfg.NodeClearance was set; false means we're on the bootstrap default
	memory            *Memory
	triggers          chan Trigger
	embedStore        *EmbeddingStore // nil if embeddings disabled
	embedClient       *llm.Client     // nil if embeddings disabled

	soulPrompt    string // from SOUL.md → BOOT.md body → default
	skillWatcher  *skillmd.Watcher
	skillGuidance map[string]*skillmd.SkillMD // guidance-only skills (no CommandDispatch)
	// environment is Config.Environment: the application's description of the
	// surroundings a run happens in, appended to planning and reflection
	// prompts. It replaced a provider whose vocabulary belonged to one
	// product, which was removed with it.
	environment func() string
	// describeTrigger is Config.DescribeTrigger: how the application words its
	// own kinds of work for the planner. Nil falls back to the built-in
	// rendering, so this is additive for every existing caller.
	describeTrigger func(Trigger) string
	intentRegistry  *IntentRegistry // DB-backed intent registry; loaded at startup
	// Per-investigation state (active skill cards, preflight result) lives on the
	// Graph (Graph.ActiveCards / Graph.Preflight), not on the Agent — concurrent
	// investigations each carry their own Graph, so nothing here is shared or raced.
	eventStore EventStore // nil if no event store

	kernel *Kernel // core runtime — owns the scheduler + investigation lifecycle

	// stopped is closed by Stop and read by Start, which is how an agent ends
	// without ending the context around it. A channel rather than a cancel func
	// because Stop can arrive before Start does.
	stopped  chan struct{}
	stopOnce sync.Once

	// DAG observation (live thought process streaming). Subscribers receive every
	// investigation's events; each event is tagged with its own Graph's SessionID
	// at emission, so consumers can route by session.
	dagMu    sync.RWMutex
	dagSubs  map[int]chan DAGEvent // subscriber ID → channel
	dagSubID int
}

/*
 * New creates an Agent with the given configuration.
 * desc: Initializes all subsystems: LLM client, tool registry, IGX gate,
 *       memory, prompts, and skill card. Returns the configured agent.
 * param: cfg - agent configuration.
 * return: pointer to the new Agent, or error.
 */
func New(cfg Config) (*Agent, error) {
	if cfg.MetadataDir == "" {
		cfg.MetadataDir = cfg.Workspace
	}

	// Resolve system prompts once at boot. Fail-closed: a missing or malformed
	// override leaves a required prompt empty, and running with an empty system
	// prompt is a security hazard — abort rather than silently degrade.
	if err := prompt.Load(cfg.DataDir); err != nil {
		log.Fatalf("[agent] prompt load failed: %v", err)
	}

	client := llm.NewClient(cfg.LLMEndpoint, cfg.LLMAPIKey, cfg.LLMModel)

	reg := toolapi.NewRegistry()

	// IGX clearance: use configured value. Before the intent registry is
	// loaded we have no concept of a "default working rank", so we start at
	// 0 (the safest possible) and LoadIntentRegistry() bumps us to the
	// registry's default rank after config/DB seeding has run.
	clearanceExplicit := cfg.NodeClearance > 0
	clrValue := 0
	if clearanceExplicit {
		clrValue = cfg.NodeClearance
	}
	clr := &localClearance{value: clrValue}

	agentDir := cfg.DataDir + "/agent"
	gate, err := gates.NewGate(gates.GateConfig{
		MaxTurns:  cfg.MaxTurns,
		RateLimit: cfg.RateLimit,
		AuditDir:  agentDir,
		Clearance: clr,
	})
	if err != nil {
		return nil, err
	}

	mem, err := NewMemory(agentDir)
	if err != nil {
		return nil, err
	}

	// Load externalized prompts
	soul := loadSoulPrompt(cfg.DataDir)
	builtinSkills := loadBuiltinSkills(cfg.BuiltinSkills)

	// Executor defaults to same client if not configured separately
	executorClient := client

	// One client per configured provider for per-request model routing. The
	// map key is the routing name (what a request selects); Type is the wire
	// protocol (defaults to the name). Model is left empty — the per-request
	// model overrides it at the call seam (see model_route.go).
	providerClients := make(map[string]*llm.Client, len(cfg.Providers))
	for name, p := range cfg.Providers {
		if p.APIKey == "" {
			log.Printf("[agent] provider %q has no api_key, skipping", name)
			continue
		}
		wire := p.Type
		if wire == "" {
			wire = name
		}
		providerClients[name] = llm.NewClientWithProvider(wire, p.Endpoint, p.APIKey, "")
	}
	if len(providerClients) > 0 {
		names := make([]string, 0, len(providerClients))
		for n := range providerClients {
			names = append(names, n)
		}
		log.Printf("[agent] model routing enabled, providers: %v", names)
	}

	a := &Agent{
		stopped:           make(chan struct{}),
		cfg:               cfg,
		llm:               client,
		executor:          executorClient,
		providerClients:   providerClients,
		registry:          reg,
		gate:              gate,
		clearance:         clr,
		clearanceExplicit: clearanceExplicit,
		memory:            mem,
		triggers:          make(chan Trigger, 16),
		dagSubs:           make(map[int]chan DAGEvent),
		skillGuidance:     builtinSkills,
		soulPrompt:        soul,
		intentRegistry:    NewIntentRegistry(),
	}

	// Wire the optional capabilities the application supplied, so the agent is
	// fully formed when New returns rather than after a dozen further calls.
	a.applyCapabilities(cfg)
	return a, nil
}

/*
 * LoadIntentRegistry populates the in-memory intent registry from the DB.
 * desc: Called from main.go after DB migrations run and config-seeded
 *       intents are in place. Requires restart to pick up DB changes.
 *       When the config did not explicitly set NodeClearance, this also
 *       resolves the default clearance to the registry's default rank
 *       (the middle of the ladder).
 * param: src - where the intent table is read from.
 * return: error if loading fails.
 */
func (a *Agent) LoadIntentRegistry(src IntentSource) error {
	if err := a.intentRegistry.Load(src); err != nil {
		return err
	}
	if !a.clearanceExplicit {
		a.clearance.Set(a.intentRegistry.DefaultRank())
	}
	return nil
}

/*
 * IntentRegistry returns the agent's intent registry for read access.
 */
func (a *Agent) Intents() *IntentRegistry { return a.intentRegistry }

/*
 * Registry returns the skill registry for external registration.
 * desc: Exposes the tool registry so callers can register additional skills.
 * return: pointer to the toolapi.Registry.
 */
func (a *Agent) Registry() *toolapi.Registry {
	return a.registry
}

/*
 * Memory returns the agent's persistent memory.
 * desc: Exposes the memory subsystem for external read/write access.
 * return: pointer to the Memory instance.
 */
func (a *Agent) Memory() *Memory {
	return a.memory
}

func (a *Agent) Workspace() string {
	return a.cfg.Workspace
}

// SoulPrompt returns the agent's resolved soul/persona prompt — the operator's
// SOUL override (dataDir/prompts.md or SOUL.md) if present, else the embedded
// default. Exposed so API-layer lanes (the vision fallback) can compose the same
// persona; the chat lane uses a.soulPrompt directly via Converse.
func (a *Agent) SoulPrompt() string {
	return a.soulPrompt
}

/*
 * Submit queues a trigger for investigation.
 * desc: Non-blocking enqueue. Drops the trigger if the queue is full.
 * param: t - the Trigger to queue.
 */
// The kernel owns the worker pool, so these are one-line accessors onto it.
// They are on Agent because a host with a dashboard adjusts concurrency while
// the process runs, and reaching through Kernel() for that is ceremony.

// InFlight reports whether any run is currently executing.
func (a *Agent) InFlight() bool { return a.kernel.InFlight() }

// SetConcurrency live-resizes how many runs execute at once.
func (a *Agent) SetConcurrency(n int) { a.kernel.SetConcurrency(n) }

// Concurrency returns the current limit on concurrent runs.
func (a *Agent) Concurrency() int { return a.kernel.Concurrency() }

/*
 * SubmitSync runs a trigger and waits for its result.
 * desc: Submit queues and returns immediately; this blocks until the run
 *       finishes, which is what an interactive caller needs — a chat request
 *       has somebody waiting on the other end.
 * param: ctx - cancels the wait, not necessarily the run.
 * param: t - the trigger.
 * return: the result, or an error.
 */
func (a *Agent) SubmitSync(ctx context.Context, t Trigger) (*SyncResult, error) {
	return a.kernel.SubmitSync(ctx, t)
}

func (a *Agent) Submit(t Trigger) {
	select {
	case a.triggers <- t:
		log.Printf("[agent] trigger queued: type=%s id=%s", t.Type, t.ID)
	default:
		log.Printf("[agent] trigger dropped (queue full): type=%s id=%s", t.Type, t.ID)
	}
}

// InitKernel builds the kernel + its built-in modules and spawns the event
// loop in a goroutine. Callers that need SubmitSync before Start blocks on
// its main loop can call InitKernel synchronously first — kernel is non-nil
// as soon as InitKernel returns. Start calls InitKernel itself, so if you
// use Start you don't need to call this.
func (a *Agent) InitKernel(ctx context.Context) {
	if a.kernel != nil {
		return
	}
	a.kernel = NewKernel(a)
	a.kernel.Register(NewHeartbeatModule(30 * time.Second))
	a.kernel.Register(&ExecutiveModule{})
	go a.kernel.Run(ctx)
}

/*
 * Start runs the agent loop: dequeue trigger, investigate, repeat.
 * desc: Blocks until ctx is cancelled. Periodically prunes expired memory entries.
 * param: ctx - context controlling the agent's lifetime.
 */
/*
 * Stop ends this agent, whatever the context it was given is doing.
 * desc: For an application that replaces its agent while the process keeps
 *       running — a model setting changed, a role changed. Without it the
 *       agent being replaced holds its kernel, its scheduler workers and its
 *       event loop until the process exits, because every rebuild is handed
 *       the same process-lifetime context.
 *
 *       Safe before Start, safe twice, safe from any goroutine. Returns as
 *       soon as the stop is signalled rather than waiting for the workers to
 *       drain, which is what a caller replacing an agent wants: the work in
 *       flight belongs to the old agent and ends with it.
 */
func (a *Agent) Stop() {
	a.stopOnce.Do(func() { close(a.stopped) })
}

func (a *Agent) Start(ctx context.Context) {
	dagLabel := "off"
	if a.cfg.DAGEnabled {
		dagLabel = "on"
	}
	log.Printf("[agent] started (model=%s, maxTurns=%d, rateLimit=%d/hr, dag=%s)",
		a.cfg.LLMModel, a.cfg.MaxTurns, a.cfg.RateLimit, dagLabel)

	// An agent can be stopped on its own, not only by ending the context it was
	// given. An application that replaces its agent — because a model setting
	// changed, or a role did — hands the replacement the same process-lifetime
	// context, so without this the one being replaced keeps its kernel, its
	// scheduler workers and this loop running until the process exits.
	//
	// Derived here rather than in New because this is where the context arrives,
	// and read through a channel closed by Stop so that stopping an agent before
	// it has started still stops it. Start is usually called in a goroutine, so
	// the two orders are both real.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-a.stopped:
			cancel()
		case <-ctx.Done():
		}
	}()

	a.InitKernel(ctx)

	// Memory prune ticker
	pruneTicker := time.NewTicker(10 * time.Minute)
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			a.gate.Close()
			log.Printf("[agent] stopped")
			return

		case trigger := <-a.triggers:
			a.kernel.Submit(trigger)

		case <-pruneTicker.C:
			if n := a.memory.Prune(); n > 0 {
				log.Printf("[agent] pruned %d expired memory entries", n)
			}
		}
	}
}

// Kernel returns the kernel instance. Used by API/CLI for SubmitSync.
func (a *Agent) Kernel() *Kernel {
	return a.kernel
}

/*
 * InitEmbeddings initializes the embedding store if embeddings are enabled.
 * desc: Must be called after all skills are registered and before Start().
 *       Gracefully falls back to no semantic routing on failure.
 * param: ctx - context for the embedding API call.
 * return: error (currently always nil due to graceful fallback).
 */
func (a *Agent) InitEmbeddings(ctx context.Context) error {
	if !a.cfg.EmbeddingsEnabled {
		return nil
	}

	endpoint := a.cfg.EmbedEndpoint
	if endpoint == "" {
		endpoint = a.cfg.LLMEndpoint
	}
	model := a.cfg.EmbedModel
	apiKey := a.cfg.EmbedAPIKey
	if apiKey == "" {
		apiKey = a.cfg.LLMAPIKey
	}

	a.embedClient = llm.NewClient(endpoint, apiKey, model)

	topK := a.cfg.EmbedTopK
	if topK <= 0 {
		topK = 8
	}
	thresh := a.cfg.EmbedThreshold
	if thresh <= 0 {
		thresh = 0.3
	}

	a.embedStore = NewEmbeddingStore(topK, thresh, a.cfg.AlwaysInclude)

	if err := a.embedStore.Load(ctx, a.embedClient, a.registry); err != nil {
		log.Printf("[agent] embedding load failed, routing disabled: %v", err)
		a.embedStore = nil
		a.embedClient = nil
		return nil // graceful fallback
	}

	log.Printf("[agent] semantic skill routing enabled (topK=%d, threshold=%.2f, always=%v)",
		topK, thresh, a.cfg.AlwaysInclude)
	return nil
}

/*
 * relevantTools returns the ranked list of executable tools (registry entries
 * with Execute methods).
 * desc: "Tools" here means things that DO work — bash, file_read, compute,
 *       etc. Uses embedding-based semantic ranking if enabled, else returns
 *       all registered tools. Guidance-only SkillMD entries (planning
 *       guidance, no Execute) are NOT included — those live in
 *       a.skillGuidance and are consumed separately via the classifier /
 *       preflight pipeline.
 * param: ctx - context for the embedding API call.
 * param: triggerText - the trigger text to rank tools against.
 * param: scope - resolved tool access scope (nil for full access).
 * return: ordered slice of tool names visible to the executive.
 */
func (a *Agent) relevantTools(ctx context.Context, trigger Trigger) []string {
	triggerText, scope := a.formatTrigger(trigger), trigger.Scope
	var base []string
	if a.embedStore == nil || a.embedClient == nil {
		base = a.registry.List()
	} else {
		ranked, err := a.embedStore.RankTools(ctx, a.embedClient, triggerText, a.registry)
		if err != nil {
			log.Printf("[agent] tool ranking failed, using all: %v", err)
			base = a.registry.List()
		} else {
			base = ranked
		}
	}

	// The agent tool is never offered to the executive/planner — that would let
	// an agent spawn an agent (unbounded recursion). It is reachable only when a
	// lane names it directly (the chat lane's chat_tools). Strip it here.
	if len(base) > 0 {
		pruned := make([]string, 0, len(base))
		for _, n := range base {
			if n != agentToolName {
				pruned = append(pruned, n)
			}
		}
		base = pruned
	}

	// A run with nobody watching does not get the tools that exist to be asked
	// for. A tool says so itself (toolapi.InteractiveOnly); this package only
	// asks whether anyone is there, which the application states by marking the
	// run autonomous.
	if a.unattended(trigger) {
		kept := base[:0]
		for _, name := range base {
			if tool, ok := a.registry.Get(name); ok && toolapi.RequiresHuman(tool) {
				continue
			}
			kept = append(kept, name)
		}
		base = kept
	}

	// Apply scope filtering — tools not in scope are invisible to the executive.
	// nil scope = full access (CLI local user).
	// Wildcard "*" in AllowedTools means all tools.
	if scope == nil || scope.AllowedTools["*"] {
		return base
	}
	filtered := base[:0]
	for _, name := range base {
		if scope.AllowedTools[name] {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

// toolSectionLines appends the body of an "## Available Tools" section to sb — one
// bullet "- **name**: desc — `params`" per registered tool whose resolved intent
// rank is within `intent`. Names in `exclude` are skipped: the self-spawn guards
// (agent everywhere; debug for Holmes) live here so every DAG node that lists tools
// to a model applies them the same way the executive's relevantTools does. The
// caller writes its own header/caption; this writes only the tool bullets.
func (a *Agent) toolSectionLines(sb *strings.Builder, intent int, exclude ...string) {
	skip := make(map[string]bool, len(exclude))
	for _, n := range exclude {
		skip[n] = true
	}
	for _, name := range a.registry.List() {
		if skip[name] {
			continue
		}
		sk, ok := a.registry.Get(name)
		if !ok {
			continue
		}
		if a.intentRegistry.ResolveToolIntent(name, sk, nil) > intent {
			continue
		}
		sb.WriteString(fmt.Sprintf("- **%s**: %s — `%s`\n", name, sk.Description(), string(sk.Parameters())))
	}
}

/*
 * Interject sends a user message to an active investigation.
 * desc: Non-blocking enqueue of a human message for injection into the
 *       investigation as an interjection reflection.
 * param: msg - the operator's message text.
 * return: false if no investigation is running or the channel is full.
 */
func (a *Agent) Interject(session, msg string) bool {
	if a.kernel == nil {
		return false
	}
	return a.kernel.Interject(session, msg)
}

// Cancel stops the running investigation for a session — the Stop button. Returns
// true if one was running. Unlike a client disconnect (which only stops the API
// handler waiting), this cancels the job's context so the DAG actually unwinds.
func (a *Agent) Cancel(session string) bool {
	if a.kernel == nil {
		return false
	}
	return a.kernel.Cancel(session)
}

/*
 * SubscribeDAG creates a subscriber channel for DAG events.
 * desc: Returns a read-only channel and an unsubscribe function. The channel
 *       receives all DAGEvent values broadcast during investigations.
 * return: read-only DAGEvent channel and cleanup function.
 */
func (a *Agent) SubscribeDAG() (<-chan DAGEvent, func()) {
	a.dagMu.Lock()
	defer a.dagMu.Unlock()

	a.dagSubID++
	id := a.dagSubID
	ch := make(chan DAGEvent, 64)
	a.dagSubs[id] = ch

	unsub := func() {
		a.dagMu.Lock()
		delete(a.dagSubs, id)
		a.dagMu.Unlock()
		// Drain any buffered events so senders don't block
		for {
			select {
			case <-ch:
			default:
				return
			}
		}
	}
	return ch, unsub
}

/*
 * dagFanOut reads from a Graph observer channel and broadcasts to all subscribers.
 * desc: Runs as a goroutine, forwarding every event from the graph observer
 *       to all registered DAG subscribers. The graph tags each event with its
 *       own SessionID so concurrent investigations stay separable downstream.
 * param: src - the source channel from the Graph observer.
 * param: graph - the investigation that owns this observer channel.
 */
func (a *Agent) dagFanOut(src <-chan DAGEvent, graph *Graph) {
	defer guardLoop("the DAG event fan-out")
	for evt := range src {
		a.broadcastDAGEvent(graph, evt)
	}
}

/*
 * broadcastDAGEvent sends an event to all DAG subscribers (non-blocking per sub).
 * desc: Drops events for slow subscribers to prevent blocking the pipeline.
 *       Tags the event with the emitting graph's SessionID when the caller
 *       hasn't set one, so subscribers can route by session. graph may be nil
 *       for paths with no investigation graph (e.g. the ReAct loop), in which
 *       case the event's own SessionID (if any) is kept as-is.
 * param: graph - the investigation that emitted the event (may be nil).
 * param: evt - the DAGEvent to broadcast.
 */
func (a *Agent) broadcastDAGEvent(graph *Graph, evt DAGEvent) {
	if evt.SessionID == "" && graph != nil {
		evt.SessionID = graph.SessionID
	}

	a.dagMu.RLock()
	defer a.dagMu.RUnlock()

	for _, ch := range a.dagSubs {
		select {
		case ch <- evt:
		default: // drop if subscriber is slow
		}
	}
}

/*
 * LLMClient returns the agent's LLM client for fallback chat.
 * desc: Exposes the reasoning LLM client for direct use by API handlers.
 * return: pointer to the llm.Client.
 */
func (a *Agent) LLMClient() *llm.Client {
	return a.llm
}

/*
 * DAGEnabled returns true if the DAG investigation engine is configured.
 * desc: Reads the DAGEnabled flag from the agent configuration.
 * return: true if DAG mode is enabled.
 */
func (a *Agent) DAGEnabled() bool {
	return a.cfg.DAGEnabled
}

/*
 * SetDAGEnabled toggles DAG mode at runtime.
 * desc: Allows live switching between DAG and ReAct execution for benchmarking.
 * param: enabled - true for DAG, false for ReAct
 */
func (a *Agent) SetDAGEnabled(enabled bool) {
	a.cfg.DAGEnabled = enabled
}

// SetVisionModel sets the default vision lane (provider name + model id). Empty
// model disables the dedicated lane.
func (a *Agent) SetVisionModel(provider, model string) {
	a.visionProvider = provider
	a.visionModel = model
}

// VisionModel returns the default vision lane (provider, model).
func (a *Agent) VisionModel() (provider, model string) {
	return a.visionProvider, a.visionModel
}

// SetChatModel sets the default chat lane (provider, model). Empty model ⇒ the
// chat lane uses the reasoning model.
func (a *Agent) SetChatModel(provider, model string) {
	a.chatProvider = provider
	a.chatModel = model
}

// ChatModel returns the default chat lane (provider, model).
func (a *Agent) ChatModel() (provider, model string) {
	return a.chatProvider, a.chatModel
}

// SetChatTools sets the default chat-lane tool allowlist, used when a request
// sends no chat_tools of its own.
func (a *Agent) SetChatTools(tools []string) { a.chatTools = tools }

// ChatTools returns the default chat-lane tool allowlist.
func (a *Agent) ChatTools() []string { return a.chatTools }

// SetRouteModel pins the model that makes the routing decision (empty ⇒ the
// executor lane). Live-applied so the config API/CLI can change it at runtime.
func (a *Agent) SetRouteModel(provider, model string) {
	a.routeProvider = provider
	a.routeModel = model
}

// RouteModel returns the pinned routing model (provider, model).
func (a *Agent) RouteModel() (provider, model string) {
	return a.routeProvider, a.routeModel
}

// SetAnswerModel pins the model that writes the FINAL answer — the aggregator
// (investigate path) and the chat lane. Empty ⇒ the heavy/reasoning lane. This is
// the model a user perceives as "the AI"; it does open-ended generation, not tool
// calls, so a thinking model is fine here. Live-applied.
func (a *Agent) SetAnswerModel(provider, model string) {
	a.answerProvider = provider
	a.answerModel = model
}

// AnswerModel returns the pinned answer model (provider, model).
func (a *Agent) AnswerModel() (provider, model string) {
	return a.answerProvider, a.answerModel
}

/*
 * DAGMode returns the configured DAG execution mode.
 * desc: Returns the DAG mode string from the agent configuration.
 * return: one of "reflect", "nReflect", or "orchestrator".
 */
func (a *Agent) DAGMode() string {
	return a.cfg.DAGMode
}

/*
 * InitSkills loads SKILL.md skills and starts the hot-reload watcher.
 * desc: Scans default and extra directories for SKILL.md files, registers
 *       them (skipping builtins), and starts a polling watcher for changes.
 * param: ctx - context controlling the watcher's lifetime.
 * param: extraDirs - additional directories to scan for SKILL.md files.
 * param: pollSec - watcher polling interval in seconds.
 * return: error if directory scanning fails.
 */
func (a *Agent) InitSkills(ctx context.Context, extraDirs []string, pollSec int) error {
	dirs := skillmd.DefaultDirs(a.cfg.DataDir, a.cfg.Workspace)
	dirs = append(dirs, extraDirs...)

	loaded, err := skillmd.LoadFromDirs(dirs, a.registry)
	if err != nil {
		return err
	}

	var toolCount, guidanceCount int
	for _, s := range loaded {
		// Skip if a builtin already has this name
		if a.registry.IsBuiltin(s.Name()) {
			log.Printf("[agent] skip SKILL.md %q: builtin exists", s.Name())
			continue
		}
		if s.HasCommandDispatch() {
			// Skills with CommandDispatch wrap a real tool — register in tool registry
			a.registry.Replace(s, "skillmd:"+s.FilePath())
			toolCount++
		} else {
			// Guidance-only skills — store separately for executive injection
			a.skillGuidance[s.Name()] = s
			guidanceCount++
		}
	}

	interval := time.Duration(pollSec) * time.Second
	w := skillmd.NewWatcher(dirs, a.registry, interval)
	for _, s := range loaded {
		if !a.registry.IsBuiltin(s.Name()) {
			w.SetManaged(s)
		}
	}
	a.skillWatcher = w
	go w.Start(ctx)

	if len(loaded) > 0 {
		log.Printf("[agent] loaded %d SKILL.md skills (%d tools, %d guidance), watcher started (%ds interval)",
			len(loaded), toolCount, guidanceCount, pollSec)
	}
	return nil
}

/*
 * ToolsInfo returns metadata for all tools/skills (dashboard).
 * desc: Delegates to the registry's ListInfo method.
 * return: slice of ToolInfo structs for all registered tools.
 */
func (a *Agent) ToolsInfo() []toolapi.ToolInfo {
	return a.registry.ListInfo()
}

/*
 * SetToolEnabled turns a tool on or off (dashboard).
 * desc: For an application whose tools run only where the agent runs. It can
 *       never grant remote reach: on means local. An application that dispatches
 *       work onto other machines wants SetToolReach instead.
 * param: name - the tool name.
 * param: enabled - true to enable, false to disable.
 * return: error if the tool is not found.
 */
func (a *Agent) SetToolEnabled(name string, enabled bool) error {
	return a.registry.SetEnabled(name, enabled)
}

/*
 * SetToolReach sets how far a tool may be called from (dashboard).
 * desc: Off means nothing may call it, local means this agent's own runs may,
 *       everywhere adds whatever the application lets call in from elsewhere.
 *       The third state is the one a boolean cannot hold: a tool that is fine
 *       to run here can be a poor thing to let a stranger trigger.
 * param: name - the tool name.
 * param: reach - the desired reach.
 * return: error if the tool is not found.
 */
func (a *Agent) SetToolReach(name string, reach toolapi.Reach) error {
	return a.registry.SetReach(name, reach)
}

/*
 * GateInfo returns current gate configuration including IGX clearance and lockdown.
 * desc: Exposes gate settings for dashboard display.
 * return: rateLimit, maxTurns, clearance level, and lockdown status.
 */
func (a *Agent) GateInfo() (rateLimit, maxTurns, clearance int, lockdown bool) {
	rl, mt := a.gate.Info()
	return rl, mt, a.clearance.Clearance(), a.gate.Lockdown()
}

/*
 * SetLLMClient hot-swaps the reasoning LLM client at runtime.
 * desc: Creates a new client with the given provider settings and updates config.
 * param: provider - the LLM provider name.
 * param: endpoint - the API endpoint URL.
 * param: apiKey - the API key.
 * param: model - the model identifier.
 */
func (a *Agent) SetLLMClient(provider, endpoint, apiKey, model string) {
	a.llm = llm.NewClientWithProvider(provider, endpoint, apiKey, model)
	a.cfg.LLMEndpoint = endpoint
	a.cfg.LLMAPIKey = apiKey
	a.cfg.LLMModel = model
}

/*
 * SetExecutorClient hot-swaps the executor LLM client at runtime.
 * desc: Creates a new client for the executor model (reflection, observer, micro-planner).
 * param: provider - the LLM provider name.
 * param: endpoint - the API endpoint URL.
 * param: apiKey - the API key.
 * param: model - the model identifier.
 */
func (a *Agent) SetExecutorClient(provider, endpoint, apiKey, model string) {
	a.executor = llm.NewClientWithProvider(provider, endpoint, apiKey, model)
}

/*
 * ExecutorClient returns the executor LLM client (for compactor etc.).
 * desc: Exposes the executor client for external use.
 * return: pointer to the executor llm.Client.
 */
func (a *Agent) ExecutorClient() *llm.Client {
	return a.executor
}

/*
 * SetClearanceChecker sets the external authorization checker.
 * desc: Configures the agent to validate tool calls against an external
 *       authorization endpoint before execution.
 * param: cc - the ClearanceChecker implementation.
 */
func (a *Agent) SetClearanceChecker(cc ClearanceChecker) {
	a.clearanceCheck = cc
}

/*
 * SetClearance updates the node's IGX clearance rank at runtime.
 * desc: Called externally to update clearance at runtime. The value is a
 *       raw rank from the intent registry — callers are responsible for
 *       resolving names to ranks before passing them in.
 * param: level - the new clearance rank.
 */
func (a *Agent) SetClearance(level int) {
	a.clearance.Set(level)
}

/*
 * NodeClearance returns the current IGX clearance level.
 * desc: Thread-safe read of the current clearance value.
 * return: the current clearance integer.
 */
func (a *Agent) NodeClearance() int {
	return a.clearance.Clearance()
}

/*
 * UpdateGate modifies gate configuration at runtime.
 * desc: Updates rate limit, max turns, clearance and lockdown as specified.
 *       nil values are left unchanged.
 *
 *       Setting the clearance marks it explicit, so a later registry load
 *       does not overwrite the operator's choice with the registry's default
 *       — which is the whole point of raising or lowering it by hand.
 * param: rateLimit - new rate limit (nil to keep current).
 * param: maxTurns - new max turns (nil to keep current).
 * param: clearance - new clearance rank (nil to keep current).
 * param: lockdown - new lockdown state (nil to keep current).
 */
func (a *Agent) UpdateGate(rateLimit, maxTurns, clearance *int, lockdown *bool) {
	a.gate.Update(rateLimit, maxTurns)
	if clearance != nil {
		a.clearance.Set(*clearance)
		a.clearanceExplicit = true
	}
	if lockdown != nil {
		a.gate.SetLockdown(*lockdown)
	}
}
