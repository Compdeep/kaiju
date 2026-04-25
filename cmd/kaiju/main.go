package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Compdeep/kaiju/internal/agent"
	"github.com/Compdeep/kaiju/internal/api"
	"github.com/Compdeep/kaiju/internal/auth"
	"github.com/Compdeep/kaiju/internal/channels"
	kaijuclr "github.com/Compdeep/kaiju/internal/clearance"
	"github.com/Compdeep/kaiju/internal/channels/cli"
	"github.com/Compdeep/kaiju/internal/channels/web"
	"github.com/Compdeep/kaiju/internal/compat/ipc"
	"github.com/Compdeep/kaiju/internal/compat/protocol"
	"github.com/Compdeep/kaiju/internal/config"
	kaijudb "github.com/Compdeep/kaiju/internal/db"
	"github.com/Compdeep/kaiju/internal/gateway"
	"github.com/Compdeep/kaiju/internal/memory"
	"github.com/Compdeep/kaiju/internal/skillhub"
	"github.com/Compdeep/kaiju/internal/workspace"
	kaijutools "github.com/Compdeep/kaiju/internal/tools"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "chat":
		runChat()
	case "serve":
		runServe()
	case "run":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: kaiju run \"query\"")
			os.Exit(1)
		}
		runOnce(strings.Join(os.Args[2:], " "))
	case "skill", "clawhub":
		runSkillCmd()
	case "user":
		runUserCmd()
	case "version":
		fmt.Printf("kaiju %s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}


func printUsage() {
	fmt.Println(`kaiju — general-purpose AI assistant

Usage:
  kaiju chat                  Interactive CLI chat
  kaiju serve [--config FILE] Start gateway daemon (API + channels)
  kaiju run "query"           One-shot query, print result, exit
  kaiju skill install <slug>  Install skill from ClawHub
  kaiju skill list            List installed skills
  kaiju skill update          Update all ClawHub skills
  kaiju skill info <slug>     Show skill details from registry
  kaiju clawhub <subcommand>  Alias for "kaiju skill" (ClawHub compatibility)
  kaiju user add|remove|list  Manage API users
  kaiju version               Print version
  kaiju help                  Show this help`)
}

// loadConfig finds and loads the config, falling back to defaults.
func loadConfig() *config.Config {
	// Check for --config flag
	configPath := ""
	for i, arg := range os.Args {
		if arg == "--config" && i+1 < len(os.Args) {
			configPath = os.Args[i+1]
			break
		}
	}

	if configPath == "" {
		configPath = os.Getenv("KAIJU_CONFIG")
	}

	if configPath != "" {
		cfg, err := config.Load(configPath)
		if err != nil {
			log.Fatalf("config: %v", err)
		}
		return cfg
	}

	// Try default locations
	for _, p := range []string{"kaiju.json", "~/.kaiju/config.json"} {
		cfg, err := config.Load(p)
		if err == nil {
			return cfg
		}
	}

	// Fall back to defaults + env vars
	cfg := config.Default()
	if key := os.Getenv("LLM_API_KEY"); key != "" {
		cfg.LLM.APIKey = key
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" && cfg.LLM.APIKey == "" {
		cfg.LLM.APIKey = key
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" && cfg.LLM.APIKey == "" {
		cfg.LLM.APIKey = key
		cfg.LLM.Provider = "anthropic"
		cfg.LLM.Endpoint = "https://api.anthropic.com"
		cfg.LLM.Model = "claude-sonnet-4-20250514"
	}
	if key := os.Getenv("OPENROUTER_API_KEY"); key != "" && cfg.LLM.APIKey == "" {
		cfg.LLM.APIKey = key
		cfg.LLM.Provider = "openrouter"
		cfg.LLM.Endpoint = "https://openrouter.ai/api/v1"
		cfg.LLM.Model = "anthropic/claude-sonnet-4"
	}
	cfg.Agent.DataDir = config.Default().Agent.DataDir
	return cfg
}

// createAgent builds the agent from config.
func createAgent(cfg *config.Config) *agent.Agent {
	// Default classifier_enabled to true when not set in config.
	// Explicit false (pointer set to false) disables preflight for testing.
	classifierEnabled := true
	if cfg.Agent.ClassifierEnabled != nil {
		classifierEnabled = *cfg.Agent.ClassifierEnabled
	}

	agentCfg := agent.Config{
		LLMEndpoint:       cfg.LLM.Endpoint,
		LLMAPIKey:         cfg.LLM.APIKey,
		LLMModel:          cfg.LLM.Model,
		MaxTurns:          cfg.Agent.MaxTurns,
		Temperature:       cfg.LLM.Temperature,
		MaxTokens:         cfg.LLM.MaxTokens,
		RateLimit:         cfg.Agent.RateLimit,
		NodeClearance:     cfg.Agent.SafetyLevel,
		NodeRole:          "node",
		DataDir:           cfg.Agent.DataDir,
		Workspace:         cfg.Agent.Workspace,
		MetadataDir:       cfg.Agent.MetadataDir,
		CLIMode:           cfg.Agent.CLIMode,
		NodeID:            "kaiju-local",
		DAGEnabled:        cfg.Agent.DAGEnabled,
		DAGMode:           cfg.Agent.DAGMode,
		MaxNodes:          cfg.Agent.MaxNodes,
		MaxPerSkill:       cfg.Agent.MaxPerSkill,
		MaxLLMCalls:       cfg.Agent.MaxLLMCalls,
		MaxObserverCalls:  cfg.Agent.MaxObserverCalls,
		BatchSize:         cfg.Agent.BatchSize,
		MaxInvestigations: cfg.Agent.MaxInvestigations,
		ExecutionMode:     cfg.Agent.ExecutionMode,
		DAGWallClock:      time.Duration(cfg.Agent.WallClockSec) * time.Second,
		ComputeTimeout:    time.Duration(cfg.Tools.Compute.TimeoutSec) * time.Second,
		ClassifierEnabled: classifierEnabled,
	}

	// Bootstrap workspace on first run
	if cfg.Agent.CLIMode {
		if err := workspace.BootstrapCLI(cfg.Agent.Workspace); err != nil {
			log.Printf("[workspace] bootstrap warning: %v", err)
		}
	} else {
		if err := workspace.Bootstrap(cfg.Agent.Workspace); err != nil {
			log.Printf("[workspace] bootstrap warning: %v", err)
		}
	}

	ag, err := agent.New(agentCfg, noopGossip{}, noopIPC{}, "kaiju-local")
	if err != nil {
		log.Fatalf("agent: %v", err)
	}

	// Set the correct LLM provider (agent.New defaults to openai)
	if cfg.LLM.Provider != "" && cfg.LLM.Provider != "openai" {
		ag.SetLLMClient(cfg.LLM.Provider, cfg.LLM.Endpoint, cfg.LLM.APIKey, cfg.LLM.Model)
	}

	// Set executor model (if configured separately, otherwise uses reasoning model)
	if cfg.Executor.Model != "" {
		execProvider := cfg.Executor.Provider
		if execProvider == "" {
			execProvider = cfg.LLM.Provider
		}
		execEndpoint := cfg.Executor.Endpoint
		if execEndpoint == "" {
			execEndpoint = cfg.LLM.Endpoint
		}
		execAPIKey := cfg.Executor.APIKey
		if execAPIKey == "" {
			execAPIKey = cfg.LLM.APIKey
		}
		ag.SetExecutorClient(execProvider, execEndpoint, execAPIKey, cfg.Executor.Model)
		log.Printf("[kaiju] executor model: %s (%s)", cfg.Executor.Model, execProvider)
	}

	// Register kaiju tools
	reg := ag.Registry()
	if cfg.Tools.Sysinfo.Enabled {
		reg.Replace(kaijutools.NewSysinfo(cfg.Agent.Workspace), "builtin")
	}
	if cfg.Tools.Compute.Enabled {
		reg.Replace(agent.NewComputeTool(ag), "builtin")
		// edit_file rides the same Coder pipeline as compute(shallow) so
		// gate it on the same config flag. Decouple later if we want file
		// edits without full compute capability.
		reg.Replace(agent.NewEditFileTool(ag), "builtin")
	}
	if cfg.Tools.Bash.Enabled {
		reg.Replace(kaijutools.NewBash(cfg.Tools.Bash.Shell, cfg.Agent.Workspace), "builtin")
	}
	if cfg.Tools.File.Enabled {
		reg.Replace(kaijutools.NewFileRead(cfg.Agent.Workspace), "builtin")
		reg.Replace(kaijutools.NewFileWrite(cfg.Agent.Workspace), "builtin")
		reg.Replace(kaijutools.NewFileList(cfg.Agent.Workspace), "builtin")
	}
	if cfg.Tools.Web.Enabled {
		reg.Replace(kaijutools.NewWebFetchWithLLM(ag.ExecutorClient()), "builtin")
		reg.Replace(kaijutools.NewWebSearchWithConfig(kaijutools.SearchConfig{
			Provider: cfg.Tools.Web.SearchProvider,
			DelaySec: cfg.Tools.Web.SearchDelaySec,
		}), "builtin")
	}

	// System tools (always enabled)
	reg.Replace(kaijutools.NewProcessList(), "builtin")
	reg.Replace(kaijutools.NewProcessKill(), "builtin")
	reg.Replace(kaijutools.NewService(cfg.Agent.Workspace), "builtin")
	reg.Replace(kaijutools.NewNetInfo(), "builtin")
	reg.Replace(kaijutools.NewEnvList(), "builtin")
	reg.Replace(kaijutools.NewDiskUsage(), "builtin")
	reg.Replace(kaijutools.NewClipboard(), "builtin")
	reg.Replace(kaijutools.NewArchive(), "builtin")
	reg.Replace(kaijutools.NewGit(), "builtin")
	reg.Replace(kaijutools.NewPanelPush(), "builtin")

	// Memory tools
	mem := ag.Memory()
	if mem != nil {
		reg.Replace(kaijutools.NewMemoryStore(mem), "builtin")
		reg.Replace(kaijutools.NewMemoryRecall(mem), "builtin")
		reg.Replace(kaijutools.NewMemorySearch(mem), "builtin")
	}

	// Load SKILL.md user skills
	if len(cfg.SkillsDirs) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := ag.InitSkills(ctx, cfg.SkillsDirs, 30); err != nil {
			log.Printf("[kaiju] skill loading: %v", err)
		}
	}

	return ag
}

// runChat starts an interactive CLI session.
func runChat() {
	cfg := loadConfig()
	if cfg.LLM.APIKey == "" {
		log.Fatal("No LLM API key configured. Set OPENAI_API_KEY, ANTHROPIC_API_KEY, or LLM_API_KEY env var.")
	}

	// CLI mode: use current working directory as workspace.
	// Metadata (blueprints, sessions, worklog) goes in .kaiju/ under cwd.
	cwd, _ := os.Getwd()
	kaijuDir := filepath.Join(cwd, ".kaiju")
	os.MkdirAll(kaijuDir, 0755)
	cfg.Agent.Workspace = cwd
	cfg.Agent.MetadataDir = kaijuDir
	cfg.Agent.CLIMode = true

	// Route log output through the CLI status line BEFORE creating the agent,
	// so skill-loading and startup logs don't corrupt the banner.
	cliCh := cli.New()
	log.SetFlags(0)
	log.SetOutput(cliCh.LogWriter())

	ag := createAgent(cfg)

	// Open DB and load intent registry so chat sees the same intents as
	// serve — including any custom ones the admin created.
	chatDB, dbErr := kaijudb.Open(filepath.Join(cfg.Agent.DataDir, "kaiju.db"))
	if dbErr == nil {
		defer chatDB.Close()
		if err := ag.LoadIntentRegistry(chatDB); err != nil {
			log.Printf("[chat] intent registry load failed: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ag.Start(ctx)

	inbox := make(chan channels.InboundMessage, 16)
	// Wire the CLI channel to the intent registry so /intent <name>
	// validates against the current intent list (builtins + customs).
	cliCh.SetIntentLister(func() []string {
		return ag.Intents().AllowedNames(-1)
	})

	// Wire session management if DB is available.
	if chatDB != nil {
		cliCh.SetSessionHandlers(
			// creator: new session
			func() (string, error) {
				id := fmt.Sprintf("cli-%d", time.Now().UnixNano())
				if err := chatDB.CreateSession(id, "cli", "local", ""); err != nil {
					return "", err
				}
				return id, nil
			},
			// lister: recent sessions
			func(limit int) ([]cli.SessionInfo, error) {
				sessions, err := chatDB.ListSessions(limit)
				if err != nil {
					return nil, err
				}
				var out []cli.SessionInfo
				for _, s := range sessions {
					age := time.Since(time.Unix(s.UpdatedAt, 0)).Truncate(time.Minute).String()
					title := s.Title
					if title == "" {
						title = "(untitled)"
					}
					out = append(out, cli.SessionInfo{ID: s.ID, Title: title, Age: age + " ago"})
				}
				return out, nil
			},
			// loader: switch to session
			func(id string) error {
				// Find by prefix match
				sessions, err := chatDB.ListSessions(100)
				if err != nil {
					return err
				}
				for _, s := range sessions {
					if strings.HasPrefix(s.ID, id) {
						cliCh.SetSessionID(s.ID)
						return nil
					}
				}
				return fmt.Errorf("session %q not found", id)
			},
		)
		// Auto-create a session on startup
		startID := fmt.Sprintf("cli-%d", time.Now().UnixNano())
		chatDB.CreateSession(startID, "cli", "local", "")
		cliCh.SetSessionID(startID)
	}

	// Build a memory manager for the CLI (reused across messages).
	var memMgr *memory.Manager
	if chatDB != nil {
		memMgr = memory.New(chatDB, ag.LLMClient(), "local")
	}

	fmt.Printf("  kaiju v%s — type /quit to exit, /intent to set safety level\n", version)

	// Message router: inbox → agent → response → cli
	go func() {
		for msg := range inbox {
			sessionID := cliCh.SessionID()

			trigger := agent.Trigger{
				Type:      "chat_query",
				AlertID:   fmt.Sprintf("cli-%d", time.Now().UnixNano()),
				Data:      mustJSON(map[string]string{"query": msg.Text}),
				Source:    "cli",
				SessionID: sessionID,
				AggMode:   -1, // auto: let reflector + compute carve-out decide
			}

			// Load conversation history from DB
			if memMgr != nil && sessionID != "" {
				if history, err := memMgr.LoadHistory(ctx, sessionID, 50); err == nil {
					trigger.History = history
				}
				memMgr.StoreMessage(sessionID, "user", msg.Text)
			}

			// Apply CLI intent toggle — resolve via the agent's registry
			// so custom intents work.
			if intentStr := cliCh.Intent(); intentStr != "" {
				intentVal := intentStringToRank(intentStr, ag.Intents())
				trigger.MaxIntent = &intentVal
			}

			result, err := ag.Kernel().SubmitSync(ctx, trigger)
			if err != nil {
				cliCh.Send(ctx, channels.OutboundMessage{
					ChannelID: "cli",
					SessionID: sessionID,
					Text:      fmt.Sprintf("[error] %v", err),
				})
				continue
			}

			// Store assistant response
			if memMgr != nil && sessionID != "" {
				memMgr.StoreMessage(sessionID, "assistant", result.Verdict)
			}

			cliCh.Send(ctx, channels.OutboundMessage{
				ChannelID: "cli",
				SessionID: sessionID,
				Text:      result.Verdict,
			})
		}
	}()

	if err := cliCh.Start(ctx, inbox); err != nil && err != context.Canceled {
		log.Printf("[cli] %v", err)
	}
}

// runServe starts the gateway daemon with all enabled channels and API.
func runServe() {
	cfg := loadConfig()
	if cfg.LLM.APIKey == "" {
		log.Println("[kaiju] WARNING: No LLM API key configured. Agent will not work until configured via UI or env var.")
	}

	ag := createAgent(cfg)

	// Open SQLite database
	kaijuDB, err := kaijudb.Open(filepath.Join(cfg.Agent.DataDir, "kaiju.db"))
	if err != nil {
		log.Fatalf("[kaiju] database: %v", err)
	}
	defer kaijuDB.Close()
	log.Printf("[kaiju] database opened: %s/kaiju.db", cfg.Agent.DataDir)

	// Seed intents from config. This is the sole source of intent definitions
	// on first run — Go code contains no defaults. INSERT OR IGNORE means
	// edits made via the admin UI survive restarts.
	if len(cfg.Agent.Intents) > 0 {
		seeds := make([]kaijudb.Intent, 0, len(cfg.Agent.Intents))
		for _, s := range cfg.Agent.Intents {
			seeds = append(seeds, kaijudb.Intent{
				Name:              s.Name,
				Rank:              s.Rank,
				Description:       s.Description,
				PromptDescription: s.PromptDescription,
				IsBuiltin:         s.Builtin,
				IsDefault:         s.Default,
			})
		}
		if err := kaijuDB.SeedIntentsFromConfig(seeds); err != nil {
			log.Fatalf("[kaiju] seed intents from config: %v", err)
		}
		log.Printf("[kaiju] seeded %d intents from config (no-op if already in DB)", len(seeds))
	}

	// Load intent registry from DB (seeded with defaults on first run)
	if err := ag.LoadIntentRegistry(kaijuDB); err != nil {
		log.Fatalf("[kaiju] load intent registry: %v", err)
	}
	log.Printf("[kaiju] intent registry loaded (%d intents)", len(ag.Intents().List()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ag.Start(ctx)

	inbox := make(chan channels.InboundMessage, 64)
	chanReg := channels.NewRegistry()

	// Register channels
	var webCh *web.Channel
	if cfg.Channels.Web.Enabled {
		webCh = web.New()
		chanReg.Register(webCh)
		go webCh.Start(ctx, inbox)
	}

	// Gateway server
	gw := gateway.New(fmt.Sprintf(":%d", cfg.Channels.Web.Port))
	mux := gw.Mux()

	if webCh != nil {
		mux.HandleFunc("/ws", webCh.Handler())
	}

	// SSE endpoint for DAG events
	mux.HandleFunc("GET /events", gateway.SSEHandler(ag))

	// Auth + JWT (always available when web UI is served)
	jwtSvc, err := auth.NewJWTService(cfg.API.JWTSecret, cfg.Agent.DataDir, 24)
	if err != nil {
		log.Fatalf("[kaiju] JWT service: %v", err)
	}

	// Auth endpoints (unprotected — login doesn't need a token)
	authAPI := api.NewAuthAPI(kaijuDB, jwtSvc)
	authMux := http.NewServeMux()
	authAPI.RegisterRoutes(authMux)
	// Login doesn't need JWT; /me does
	mux.Handle("/api/v1/auth/login", authMux)
	mux.Handle("/api/v1/auth/me", gateway.WithJWTAuth(jwtSvc)(authMux))

	// Management APIs (JWT-protected — scopes, groups, users, intents)
	scopeAPI := api.NewScopeAPI(kaijuDB)
	groupAPI := api.NewGroupAPI(kaijuDB)
	userAPI := api.NewUserAPI(kaijuDB)
	intentAPI := api.NewIntentAPI(kaijuDB, ag)

	mgmtMux := http.NewServeMux()
	scopeAPI.RegisterRoutes(mgmtMux)
	groupAPI.RegisterRoutes(mgmtMux)
	userAPI.RegisterRoutes(mgmtMux)
	intentAPI.RegisterRoutes(mgmtMux)

	mux.Handle("/api/v1/scopes", gateway.WithJWTAuth(jwtSvc)(mgmtMux))
	mux.Handle("/api/v1/scopes/", gateway.WithJWTAuth(jwtSvc)(mgmtMux))
	mux.Handle("/api/v1/groups", gateway.WithJWTAuth(jwtSvc)(mgmtMux))
	mux.Handle("/api/v1/groups/", gateway.WithJWTAuth(jwtSvc)(mgmtMux))
	mux.Handle("/api/v1/users", gateway.WithJWTAuth(jwtSvc)(mgmtMux))
	mux.Handle("/api/v1/users/", gateway.WithJWTAuth(jwtSvc)(mgmtMux))
	mux.Handle("/api/v1/intents", gateway.WithJWTAuth(jwtSvc)(mgmtMux))
	mux.Handle("/api/v1/intents/", gateway.WithJWTAuth(jwtSvc)(mgmtMux))
	mux.Handle("/api/v1/tool-intents", gateway.WithJWTAuth(jwtSvc)(mgmtMux))
	mux.Handle("/api/v1/tool-intents/", gateway.WithJWTAuth(jwtSvc)(mgmtMux))

	// Clearance checker — load endpoints from DB, register with agent
	clrChecker := kaijuclr.NewChecker()
	if endpoints, err := kaijuDB.ListClearanceEndpoints(); err == nil {
		for _, ep := range endpoints {
			clrChecker.SetEndpoint(kaijuclr.Endpoint{
				ToolName: ep.ToolName, URL: ep.URL,
				TimeoutMs: ep.TimeoutMs, Headers: ep.Headers,
			})
		}
		if len(endpoints) > 0 {
			log.Printf("[kaiju] loaded %d clearance endpoints", len(endpoints))
		}
	}
	ag.SetClearanceChecker(clrChecker)

	// Execution API routes (always available — JWT-protected)
	apiHandler := api.New(ag, cfg.Agent.SafetyLevel, kaijuDB, ag.LLMClient(), clrChecker)
	execMux := http.NewServeMux()
	apiHandler.RegisterRoutes(execMux)
	mux.Handle("/api/v1/execute", gateway.WithJWTAuth(jwtSvc)(execMux))
	mux.Handle("/api/v1/interject", gateway.WithJWTAuth(jwtSvc)(execMux))
	mux.Handle("/api/v1/tools", gateway.WithJWTAuth(jwtSvc)(execMux))
	mux.Handle("/api/v1/status", gateway.WithJWTAuth(jwtSvc)(execMux))
	// Session + memory + clearance routes (JWT-protected)
	mux.Handle("/api/v1/sessions", gateway.WithJWTAuth(jwtSvc)(execMux))
	mux.Handle("/api/v1/sessions/", gateway.WithJWTAuth(jwtSvc)(execMux))
	mux.Handle("/api/v1/memories", gateway.WithJWTAuth(jwtSvc)(execMux))
	mux.Handle("/api/v1/memories/", gateway.WithJWTAuth(jwtSvc)(execMux))
	mux.Handle("/api/v1/clearance", gateway.WithJWTAuth(jwtSvc)(execMux))
	mux.Handle("/api/v1/clearance/", gateway.WithJWTAuth(jwtSvc)(execMux))
	mux.Handle("/api/v1/workspace/files", gateway.WithJWTAuth(jwtSvc)(execMux))
	// Serve uses token query param since browser <img>/<video>/iframes can't send Auth headers
	mux.Handle("/api/v1/workspace/serve", gateway.WithJWTAuthOrQuery(jwtSvc)(execMux))
	// Live preview serves from workspace/code/ — sub-resources (JS, CSS, images) can't carry auth tokens,
	// so live preview is served without JWT. The main app is already authenticated.
	mux.Handle("/api/v1/workspace/live/", execMux)

	// Config API (available without JWT — needed for initial setup via UI)
	cfgPath := ""
	for i, arg := range os.Args {
		if arg == "--config" && i+1 < len(os.Args) {
			cfgPath = os.Args[i+1]
			break
		}
	}
	configAPI := api.NewConfigAPI(cfg, cfgPath, ag)
	configAPI.RegisterRoutes(mux)

	// Health check
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Web UI (embedded static files)
	mux.Handle("/", gateway.WebUIHandler())

	// Message router goroutine
	go func() {
		for msg := range inbox {
			go func(msg channels.InboundMessage) {
				trigger := agent.Trigger{
					Type:    "chat_query",
					AlertID: fmt.Sprintf("%s-%d", msg.ChannelID, time.Now().UnixNano()),
					Data:    mustJSON(map[string]string{"query": msg.Text}),
					Source:  msg.ChannelID,
					AggMode: -1, // auto: let reflector + compute carve-out decide
				}

				result, err := ag.Kernel().SubmitSync(ctx, trigger)
				verdictText := ""
				if err != nil {
					verdictText = fmt.Sprintf("[error] %v", err)
				} else {
					verdictText = result.Verdict
				}

				ch, ok := chanReg.Get(msg.ChannelID)
				if ok {
					ch.Send(ctx, channels.OutboundMessage{
						ChannelID:   msg.ChannelID,
						SessionID:   msg.SessionID,
						RecipientID: msg.SenderID,
						Text:        verdictText,
					})
				}
			}(msg)
		}
	}()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("[kaiju] shutting down...")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		gw.Shutdown(shutdownCtx)
		cancel()
	}()

	log.Printf("[kaiju] v%s serving on :%d (api=%v)", version, cfg.Channels.Web.Port, cfg.API.Enabled)
	if err := gw.Start(); err != nil && err.Error() != "http: Server closed" {
		log.Fatalf("[gateway] %v", err)
	}
}

// runOnce executes a single query and exits.
func runOnce(query string) {
	cfg := loadConfig()
	if cfg.LLM.APIKey == "" {
		log.Fatal("No LLM API key configured. Set OPENAI_API_KEY, ANTHROPIC_API_KEY, or LLM_API_KEY env var.")
	}

	// Mirror runChat: use cwd as workspace and .kaiju/ under cwd for metadata.
	// Users invoking `kaiju run` from a project directory expect the agent to
	// operate in that directory, not the default ~/.kaiju/workspace.
	cwd, _ := os.Getwd()
	kaijuDir := filepath.Join(cwd, ".kaiju")
	os.MkdirAll(kaijuDir, 0755)
	cfg.Agent.Workspace = cwd
	cfg.Agent.MetadataDir = kaijuDir
	cfg.Agent.CLIMode = true

	ag := createAgent(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Agent.WallClockSec)*time.Second)
	defer cancel()

	// Initialise the kernel synchronously (Agent.Start does the same thing
	// but blocks its goroutine on other work; by calling InitKernel here we
	// guarantee ag.Kernel() is ready before SubmitSync, with no polling).
	ag.InitKernel(ctx)

	// `kaiju run` is a deliberate one-shot action, not a passive observation.
	// Lift intent to the config's safety level so service/bash actions aren't
	// gate-blocked. Preflight can still refine downward if the query turns
	// out to be read-only.
	runIntent := cfg.Agent.SafetyLevel
	if runIntent <= 0 {
		runIntent = 100
	}

	trigger := agent.Trigger{
		Type:      "chat_query",
		AlertID:   fmt.Sprintf("run-%d", time.Now().UnixNano()),
		Data:      mustJSON(map[string]string{"query": query}),
		Source:    "cli",
		AggMode:   -1, // auto: let reflector + compute carve-out decide
		MaxIntent: &runIntent,
	}

	result, err := ag.Kernel().SubmitSync(ctx, trigger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(result.Verdict)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// intentStringToRank resolves an intent name to its rank using a registry
// loaded from the given database. Unknown or empty names fall back to the
// middle rank in the registry (the "default working level"). Go code does
// not know specific intent names — everything flows through the registry.
func intentStringToRank(s string, registry *agent.IntentRegistry) int {
	if s != "" {
		if i, ok := registry.ByName(s); ok {
			return i.Rank
		}
	}
	return registry.DefaultRank()
}

// loadIntentRegistry opens a minimal registry from an already-open DB.
// Used by CLI commands that need to parse intent names without standing up a full agent.
func loadIntentRegistry(database *kaijudb.DB) *agent.IntentRegistry {
	reg := agent.NewIntentRegistry()
	_ = reg.Load(database) // errors are logged inside; fall back to empty registry
	return reg
}

// runUserCmd handles `kaiju user add|remove|list`.
func runUserCmd() {
	cfg := loadConfig()
	database, err := kaijudb.Open(filepath.Join(cfg.Agent.DataDir, "kaiju.db"))
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer database.Close()

	if len(os.Args) < 3 {
		fmt.Println(`usage:
  kaiju user add <username>      Add a user (interactive)
  kaiju user remove <username>   Remove a user
  kaiju user list                List all users`)
		os.Exit(1)
	}

	// Load intent registry from the DB so name parsing works for custom intents.
	intentReg := loadIntentRegistry(database)

	switch os.Args[2] {
	case "add":
		if len(os.Args) < 4 {
			names := intentReg.AllowedNames(-1)
			fmt.Fprintf(os.Stderr, "usage: kaiju user add <username> [--intent %s] [--scopes admin,standard,readonly]\n", strings.Join(names, "|"))
			os.Exit(1)
		}
		username := os.Args[3]

		// Parse flags. Default to the registry's middle rank (the default working level).
		maxIntent := intentReg.DefaultRank()
		var scopes []string
		for i := 4; i < len(os.Args); i++ {
			switch os.Args[i] {
			case "--intent":
				if i+1 < len(os.Args) {
					i++
					maxIntent = intentStringToRank(os.Args[i], intentReg)
				}
			case "--scopes":
				if i+1 < len(os.Args) {
					i++
					scopes = strings.Split(os.Args[i], ",")
				}
			}
		}

		// First user gets admin scope by default. Subsequent users get no scopes (deny all).
		if len(scopes) == 0 {
			if count, err := database.UserCount(); err == nil && count == 0 {
				scopes = []string{"admin"}
				// Admin gets the highest available intent rank.
				if list := intentReg.List(); len(list) > 0 {
					maxIntent = list[len(list)-1].Rank
				}
				log.Printf("[kaiju] first user — assigning admin scope")
			}
		}

		// Read password
		fmt.Print("Password: ")
		var password string
		fmt.Scanln(&password)
		if password == "" {
			fmt.Fprintln(os.Stderr, "password cannot be empty")
			os.Exit(1)
		}

		if err := database.CreateUser(username, password, maxIntent, scopes); err != nil {
			log.Fatalf("add user: %v", err)
		}
		fmt.Printf("user %q created (intent=%d, scopes=%v)\n", username, maxIntent, scopes)

	case "remove":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: kaiju user remove <username>")
			os.Exit(1)
		}
		if err := database.DeleteUser(os.Args[3]); err != nil {
			log.Fatalf("remove user: %v", err)
		}
		fmt.Printf("user %q removed\n", os.Args[3])

	case "list":
		users, err := database.ListUsers()
		if err != nil {
			log.Fatalf("list users: %v", err)
		}
		if len(users) == 0 {
			fmt.Println("no users registered")
			return
		}
		fmt.Printf("%-20s %-12s %-10s %s\n", "USERNAME", "INTENT", "DISABLED", "SCOPES")
		for _, u := range users {
			// Look up the intent name via the registry. Unknown ranks render
			// as "rank(N)" — Go has no hardcoded name mapping.
			intentName := intentReg.NameByRank(u.MaxIntent)
			if intentName == "" {
				intentName = fmt.Sprintf("rank(%d)", u.MaxIntent)
			}
			fmt.Printf("%-20s %-12s %-10v %s\n", u.Username, intentName, u.Disabled, strings.Join(u.Scopes, ","))
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown user command: %s\n", os.Args[2])
		os.Exit(1)
	}
}

// ─── Skill management ───────────────────────────────────────────────────────

func runSkillCmd() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: kaiju skill <install|list|update|info> [args]")
		os.Exit(1)
	}

	cfg := loadConfig()
	// Use first skills dir that exists, or create default
	skillsDir := ""
	for _, d := range cfg.SkillsDirs {
		if d != "" {
			skillsDir = d
			break
		}
	}
	if skillsDir == "" {
		skillsDir = filepath.Join(cfg.Agent.DataDir, "skills")
	}
	os.MkdirAll(skillsDir, 0755)

	client := skillhub.NewClient("")

	sub := os.Args[2]
	switch sub {
	case "install":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: kaiju skill install <owner/slug> or <slug>")
			os.Exit(1)
		}
		slug := os.Args[3]
		// Strip owner prefix if given (ivangdavila/playwright → playwright)
		if parts := strings.Split(slug, "/"); len(parts) == 2 {
			slug = parts[1]
		}

		// Fetch info first
		fmt.Printf("Fetching %s from ClawHub...\n", slug)
		info, err := client.Info(slug)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("  %s v%s by %s\n", info.Skill.DisplayName, info.LatestVersion.Version, info.Owner.Handle)
		fmt.Printf("  %s\n", info.Skill.Summary)

		// Check if already installed
		existing := skillhub.InstalledVersion(filepath.Join(skillsDir, slug))
		if existing != "" {
			if existing == info.LatestVersion.Version {
				fmt.Printf("  Already installed (v%s)\n", existing)
				return
			}
			fmt.Printf("  Updating v%s → v%s\n", existing, info.LatestVersion.Version)
		}

		// Download and extract
		version, files, err := client.Install(slug, skillsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("  Installed v%s (%d files)\n", version, len(files))
		for _, f := range files {
			fmt.Printf("    %s\n", f)
		}

		// Check binary dependencies
		if len(info.Metadata.OS) > 0 {
			fmt.Printf("  Supported OS: %s\n", strings.Join(info.Metadata.OS, ", "))
		}

	case "list":
		found := false
		for _, dir := range cfg.SkillsDirs {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				sdir := filepath.Join(dir, e.Name())
				if _, err := os.Stat(filepath.Join(sdir, "SKILL.md")); err != nil {
					continue
				}
				if !found {
					fmt.Println("Installed skills:\n")
					found = true
				}
				version := skillhub.InstalledVersion(sdir)
				slug := skillhub.InstalledSlug(sdir)
				source := "local"
				if slug != "" {
					source = "clawhub"
				}
				vstr := ""
				if version != "" {
					vstr = fmt.Sprintf(" v%s", version)
				}
				fmt.Printf("  %-25s %s%s (%s)\n", e.Name(), source, vstr, sdir)
			}
		}
		if !found {
			fmt.Println("No skills installed")
		}

	case "update":
		updated := 0
		checked := 0
		for _, dir := range cfg.SkillsDirs {
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				sdir := filepath.Join(dir, e.Name())
				slug := skillhub.InstalledSlug(sdir)
				if slug == "" {
					continue
				}
				checked++
				current := skillhub.InstalledVersion(sdir)

				info, err := client.Info(slug)
				if err != nil {
					fmt.Printf("  %s: error checking — %v\n", slug, err)
					continue
				}

				if current == info.LatestVersion.Version {
					fmt.Printf("  %s: up to date (v%s)\n", slug, current)
					continue
				}

				fmt.Printf("  %s: updating v%s → v%s...\n", slug, current, info.LatestVersion.Version)
				version, _, err := client.Install(slug, dir)
				if err != nil {
					fmt.Printf("  %s: update failed — %v\n", slug, err)
					continue
				}
				fmt.Printf("  %s: updated to v%s\n", slug, version)
				updated++
			}
		}
		if checked == 0 {
			fmt.Println("No ClawHub skills installed")
		} else if updated == 0 {
			fmt.Println("All ClawHub skills are up to date")
		}

	case "info":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: kaiju skill info <slug>")
			os.Exit(1)
		}
		slug := os.Args[3]
		if parts := strings.Split(slug, "/"); len(parts) == 2 {
			slug = parts[1]
		}

		info, err := client.Info(slug)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("%s\n", info.Skill.DisplayName)
		fmt.Printf("  slug:      %s\n", info.Skill.Slug)
		fmt.Printf("  version:   %s\n", info.LatestVersion.Version)
		fmt.Printf("  license:   %s\n", info.LatestVersion.License)
		fmt.Printf("  author:    %s (%s)\n", info.Owner.DisplayName, info.Owner.Handle)
		fmt.Printf("  downloads: %d\n", info.Skill.Stats.Downloads)
		fmt.Printf("  stars:     %d\n", info.Skill.Stats.Stars)
		fmt.Printf("  os:        %s\n", strings.Join(info.Metadata.OS, ", "))
		fmt.Printf("\n%s\n", info.Skill.Summary)
		if info.LatestVersion.Changelog != "" {
			fmt.Printf("\nChangelog: %s\n", info.LatestVersion.Changelog)
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown skill command: %s\n", sub)
		fmt.Fprintln(os.Stderr, "usage: kaiju skill <install|list|update|info> [args]")
		os.Exit(1)
	}
}

// ─── No-op implementations for fleet interfaces ─────────────────────────────

type noopGossip struct{}

func (noopGossip) PublishAlert(context.Context, []byte) error  { return nil }
func (noopGossip) PublishMurmur(context.Context, []byte) error { return nil }
func (noopGossip) Sequencer() *protocol.Sequencer              { return protocol.NewSequencer("builtin") }

type noopIPC struct{}

func (noopIPC) Send(ipc.Envelope) error { return nil }

// Ensure these satisfy the agent's interfaces at compile time.
var _ agent.GossipPublisher = noopGossip{}
var _ agent.IPCSender = noopIPC{}
