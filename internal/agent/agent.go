package agent

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Compdeep/kaiju/internal/agent/gates"
	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/agent/skillmd"
	"github.com/Compdeep/kaiju/internal/agent/tools"
	"github.com/Compdeep/kaiju/internal/compat/ipc"
	"github.com/Compdeep/kaiju/internal/compat/protocol"
	"github.com/Compdeep/kaiju/internal/compat/store"
	"github.com/Compdeep/kaiju/internal/db"
)

/*
 * GossipPublisher is the interface for publishing gossip messages.
 * desc: Provides methods for broadcasting alert and murmur messages
 *       to fleet peers, plus access to the message sequencer.
 */
type GossipPublisher interface {
	PublishAlert(ctx context.Context, data []byte) error
	PublishMurmur(ctx context.Context, data []byte) error
	Sequencer() *protocol.Sequencer
}

/*
 * IPCSender is the interface for sending IPC messages to the C++ service.
 * desc: Single-method interface for envelope-based IPC communication.
 */
type IPCSender interface {
	Send(env ipc.Envelope) error
}


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
 * desc: Contains the trigger type, alert ID, raw data payload, source peer,
 *       DAG mode override, data directory override, IGX intent cap, tool
 *       access scope, session ID, and conversation history.
 */
type Trigger struct {
	Type      string          `json:"type"`       // "chat_query", "api_query", "scheduled", "event", "command"
	AlertID   string          `json:"alert_id"`
	Data      json.RawMessage `json:"data"`
	Source    string          `json:"source"`      // peer ID or "local"
	DAGMode   string          `json:"dag_mode"`    // optional override: "reflect", "nReflect", "orchestrator"
	DataDir   string          `json:"data_dir"`    // override data dir for retrieval skills (relay/gateway use temp path)
	MaxIntent *int            `json:"max_intent,omitempty"` // optional IGX cap (can only lower intent, never escalate)
	Scope     *ResolvedScope  `json:"scope,omitempty"`      // tool access scope (nil = full access)
	SessionID string          `json:"session_id,omitempty"` // conversation session for memory
	History   []llm.Message   `json:"history,omitempty"`    // conversation history
	AggMode       int    `json:"agg_mode,omitempty"`        // 0=skip aggregator, 1=executor model (default), 2=reasoning model
	ExecutionMode string `json:"execution_mode,omitempty"` // per-request override: "interactive" or "autonomous"
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
 *       their structural intent (never auto — autonomous alerts are hardcoded).
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
 * Config holds agent configuration.
 * desc: Contains LLM endpoint settings, rate limits, IGX clearance,
 *       DAG engine parameters, embedding configuration, and paths.
 */
type Config struct {
	LLMEndpoint   string
	LLMAPIKey     string
	LLMModel      string
	MaxTurns      int
	Temperature   float64
	MaxTokens     int
	RateLimit     int
	NodeClearance int    // IGX clearance (0 = default 1)
	NodeRole      string // "node" or "coordinator"
	DataDir       string
	Workspace     string    // where files are written (cwd in CLI mode, sandbox in web mode)
	MetadataDir   string    // where blueprints, worklog, sessions live (.kaiju/ in CLI, same as workspace in web)
	CLIMode       bool      // true = workspace is cwd, no project/ prefix, .kaiju/ for metadata
	ExecutiveMode   string // "structured" (text JSON) or "native" (function calling)
	NodeID        string

	// DAG engine (optimistic parallel investigation)
	DAGEnabled   bool
	DAGMode      string // "reflect", "nReflect", "orchestrator" (default: "orchestrator")
	MaxNodes     int
	MaxPerSkill  int
	MaxLLMCalls  int
	MaxObserverCalls int  // separate budget for observer LLM calls (default: 50)
	BatchSize    int   // nodes completed before injecting reflection in nReflect mode (default: 5)
	MaxInvestigations int // max investigation cycles (Holmes + fix attempts) before forcing conclude (default: 1)
	MaxHolmesIters int // max ReAct iterations per Holmes investigation (default: 5)
	ExecutionMode  string // "interactive" (chat allowed) or "autonomous" (always investigate)
	DAGWallClock   time.Duration

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
 *       embeddings, skill watching, fleet context, and live DAG streaming.
 */
type Agent struct {
	cfg         Config
	llm         *llm.Client     // reasoning model (executive, aggregator, classifier)
	executor    *llm.Client     // executor model (reflection, observer, micro-planner)
	registry    *tools.Registry
	gate        *gates.Gate
	clearanceCheck ClearanceChecker // external authorization (nil = no check)
	clearance         *localClearance // IGX node clearance
	clearanceExplicit bool            // true if cfg.NodeClearance was set; false means we're on the bootstrap default
	memory      *Memory
	gossip      GossipPublisher
	ipc         IPCSender
	triggers    chan Trigger
	embedStore  *EmbeddingStore // nil if embeddings disabled
	embedClient *llm.Client    // nil if embeddings disabled

	soulPrompt    string // from SOUL.md → BOOT.md body → default
	executivePrompt string // from executive.md → default
	skillWatcher  *skillmd.Watcher
	skillGuidance map[string]*skillmd.SkillMD // guidance-only skills (no CommandDispatch)
	fleet         FleetContextProvider // nil on standalone nodes
	capabilities  CapabilityRegistry   // composable prompt cards
	intentRegistry *IntentRegistry     // DB-backed intent registry; loaded at startup
	// NOTE: activeCards and preflight live on the Agent singleton, not on the
	// per-investigation Graph. Concurrent investigations will race and clobber
	// each other's state (last writer wins). Kaiju's normal runtime serializes
	// investigations via a.investigating, so this is latent under current usage.
	// Fix would move both fields onto Graph alongside Gaps.
	activeCards []string         // selected per-investigation by preflight (formerly classifier)
	preflight   *PreflightResult // pre-plan result (mode/intent/categories/skills)
	eventStore    *store.Store          // nil if no event store

	interjections chan string     // user messages during active investigation
	investigating atomic.Bool    // true while investigate is executing
	kernel        *Kernel          // core runtime — owns investigation lifecycle

	// DAG observation (live thought process streaming)
	dagMu      sync.RWMutex
	dagSubs    map[int]chan DAGEvent // subscriber ID → channel
	dagSubID   int
	dagGraph   *Graph  // current active graph (nil when idle)
	dagAlertID string
}

/*
 * SetEventStore sets the event store for persisting investigations and actions.
 * desc: Assigns the store used for recording investigation metadata and
 *       tool execution audit trails.
 * param: s - pointer to the event store.
 */
func (a *Agent) SetEventStore(s *store.Store) {
	a.eventStore = s
}

/*
 * SetFleet sets the fleet context provider.
 * desc: Call after New() for coordinator nodes to enable fleet situational awareness.
 * param: f - the fleet context provider.
 */
func (a *Agent) SetFleet(f FleetContextProvider) {
	a.fleet = f
}

/*
 * New creates an Agent with the given configuration.
 * desc: Initializes all subsystems: LLM client, tool registry, IGX gate,
 *       memory, prompts, and capability cards. Returns the configured agent.
 * param: cfg - agent configuration.
 * param: gossip - gossip publisher for fleet communication.
 * param: ipcSender - IPC sender for C++ service communication.
 * param: nodeID - this node's unique identifier.
 * return: pointer to the new Agent, or error.
 */
func New(cfg Config, gossip GossipPublisher, ipcSender IPCSender, nodeID string) (*Agent, error) {
	cfg.NodeID = nodeID
	if cfg.MetadataDir == "" {
		cfg.MetadataDir = cfg.Workspace
	}

	client := llm.NewClient(cfg.LLMEndpoint, cfg.LLMAPIKey, cfg.LLMModel)

	reg := tools.NewRegistry()

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
	soul := loadSoulPrompt(cfg.DataDir, cfg.CustomSystemPrompt)
	executive := loadExecutivePrompt(cfg.DataDir)
	caps := loadCapabilities(cfg.DataDir)

	// Executor defaults to same client if not configured separately
	executorClient := client

	return &Agent{
		cfg:               cfg,
		llm:               client,
		executor:          executorClient,
		registry:          reg,
		gate:              gate,
		clearance:         clr,
		clearanceExplicit: clearanceExplicit,
		memory:            mem,
		gossip:            gossip,
		ipc:               ipcSender,
		triggers:          make(chan Trigger, 16),
		interjections:     make(chan string, 8),
		dagSubs:           make(map[int]chan DAGEvent),
		skillGuidance:     make(map[string]*skillmd.SkillMD),
		soulPrompt:        soul,
		executivePrompt:     executive,
		capabilities:      caps,
		intentRegistry:    NewIntentRegistry(),
	}, nil
}

/*
 * LoadIntentRegistry populates the in-memory intent registry from the DB.
 * desc: Called from main.go after DB migrations run and config-seeded
 *       intents are in place. Requires restart to pick up DB changes.
 *       When the config did not explicitly set NodeClearance, this also
 *       resolves the default clearance to the registry's default rank
 *       (the middle of the ladder).
 * param: database - the DB instance.
 * return: error if loading fails.
 */
func (a *Agent) LoadIntentRegistry(database *db.DB) error {
	if err := a.intentRegistry.Load(database); err != nil {
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
 * return: pointer to the tools.Registry.
 */
func (a *Agent) Registry() *tools.Registry {
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

/*
 * Submit queues a trigger for investigation.
 * desc: Non-blocking enqueue. Drops the trigger if the queue is full.
 * param: t - the Trigger to queue.
 */
func (a *Agent) Submit(t Trigger) {
	select {
	case a.triggers <- t:
		log.Printf("[agent] trigger queued: type=%s alert=%s", t.Type, t.AlertID)
	default:
		log.Printf("[agent] trigger dropped (queue full): type=%s alert=%s", t.Type, t.AlertID)
	}
}

/*
 * Start runs the agent loop: dequeue trigger, investigate, repeat.
 * desc: Blocks until ctx is cancelled. Periodically prunes expired memory entries.
 * param: ctx - context controlling the agent's lifetime.
 */
func (a *Agent) Start(ctx context.Context) {
	dagLabel := "off"
	if a.cfg.DAGEnabled {
		dagLabel = "on"
	}
	log.Printf("[agent] started (model=%s, maxTurns=%d, rateLimit=%d/hr, dag=%s)",
		a.cfg.LLMModel, a.cfg.MaxTurns, a.cfg.RateLimit, dagLabel)

	// Initialize kernel with built-in modules
	a.kernel = NewKernel(a)
	a.kernel.Register(NewHeartbeatModule(90 * time.Second))
	a.kernel.Register(&ExecutiveModule{})
	go a.kernel.Run(ctx)

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
func (a *Agent) relevantTools(ctx context.Context, triggerText string, scope *ResolvedScope) []string {
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

/*
 * Interject sends a user message to an active investigation.
 * desc: Non-blocking enqueue of a human message for injection into the
 *       investigation as an interjection reflection.
 * param: msg - the operator's message text.
 * return: false if no investigation is running or the channel is full.
 */
func (a *Agent) Interject(msg string) bool {
	if !a.investigating.Load() {
		return false
	}
	select {
	case a.interjections <- msg:
		return true
	default:
		return false // channel full
	}
}

/*
 * IsInvestigating returns true while an investigation is executing.
 * desc: Atomic read of the investigating flag.
 * return: true if an investigation is currently active.
 */
func (a *Agent) IsInvestigating() bool {
	return a.investigating.Load()
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
 * DAGSnapshot returns the current graph state for new SSE connections.
 * desc: Thread-safe snapshot of the active investigation graph.
 * return: nodes slice, alert ID, and whether an investigation is active.
 */
func (a *Agent) DAGSnapshot() (nodes []*NodeInfo, alertID string, active bool) {
	a.dagMu.RLock()
	defer a.dagMu.RUnlock()

	if a.dagGraph == nil {
		return nil, "", false
	}
	return a.dagGraph.Snapshot(), a.dagAlertID, true
}

/*
 * dagFanOut reads from a Graph observer channel and broadcasts to all subscribers.
 * desc: Runs as a goroutine, forwarding every event from the graph observer
 *       to all registered DAG subscribers.
 * param: src - the source channel from the Graph observer.
 */
func (a *Agent) dagFanOut(src <-chan DAGEvent) {
	for evt := range src {
		a.broadcastDAGEvent(evt)
	}
}

/*
 * broadcastDAGEvent sends an event to all DAG subscribers (non-blocking per sub).
 * desc: Drops events for slow subscribers to prevent blocking the pipeline.
 * param: evt - the DAGEvent to broadcast.
 */
func (a *Agent) broadcastDAGEvent(evt DAGEvent) {
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
func (a *Agent) ToolsInfo() []tools.ToolInfo {
	return a.registry.ListInfo()
}

/*
 * SetToolEnabled toggles a tool on/off (dashboard).
 * desc: Enables or disables a specific tool by name.
 * param: name - the tool name.
 * param: enabled - true to enable, false to disable.
 * return: error if the tool is not found.
 */
func (a *Agent) SetToolEnabled(name string, enabled bool) error {
	return a.registry.SetEnabled(name, enabled)
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
 * desc: Updates rate limit, max turns, and lockdown status as specified.
 *       nil values are left unchanged.
 * param: rateLimit - new rate limit (nil to keep current).
 * param: maxTurns - new max turns (nil to keep current).
 * param: lockdown - new lockdown state (nil to keep current).
 */
func (a *Agent) UpdateGate(rateLimit, maxTurns *int, lockdown *bool) {
	a.gate.Update(rateLimit, maxTurns)
	if lockdown != nil {
		a.gate.SetLockdown(*lockdown)
	}
}

/*
 * investigate dispatches to the DAG engine or the ReAct loop.
 * desc: Sets the investigating flag, runs the appropriate engine based on
 *       configuration, and drains pending interjections on completion.
 * param: ctx - context for the investigation.
 * param: trigger - the investigation trigger.
 */
func (a *Agent) investigate(ctx context.Context, trigger Trigger) {
	a.investigating.Store(true)
	defer func() {
		a.investigating.Store(false)
		// Drain any pending interjections
		for {
			select {
			case <-a.interjections:
			default:
				return
			}
		}
	}()

	if a.cfg.DAGEnabled {
		a.runDAG(ctx, trigger)
		return
	}
	a.investigateReAct(ctx, trigger)
}
