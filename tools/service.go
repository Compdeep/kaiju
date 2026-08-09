package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

/*
 * Service is a lightweight process manager tool.
 * desc: Spawns long-running processes in detached sessions, tracks them
 *       in a JSON registry, and exposes start/stop/restart/status/logs/list/remove
 *       actions to the executive. Fixes the nohup-blocks-investigation bug —
 *       the executive uses this instead of bash for any process that doesn't
 *       terminate quickly. Only manages processes kaiju spawns itself; it
 *       does NOT track systemd, pm2, or other OS-managed services.
 */
type Service struct {
	workspace string
	mu        sync.Mutex // serializes registry file writes
	stopPoll  chan struct{}
	crashes   map[string]int // name → consecutive fast-crash count (auto-restart backoff)
}

// Compile-time interface check
var _ toolapi.Tool = (*Service)(nil)

func NewService(workspace string) *Service {
	s := &Service{workspace: workspace, stopPoll: make(chan struct{}), crashes: map[string]int{}}
	go s.healthLoop()
	return s
}

// maxFastCrashes: give up auto-restart after this many deaths within minUptime of
// starting — otherwise a service that can't start (bad command, held port) loops
// forever. portOpen lets us skip a restart when something already serves the port.
const (
	minUptime      = 15 * time.Second
	maxFastCrashes = 5
)

// portOpen reports whether 127.0.0.1:port accepts a TCP connection — i.e. some
// process is already serving it. Used to avoid respawning a host into a
// bind-conflict loop when an orphan from a prior run still holds the port.
func portOpen(port int) bool {
	if port <= 0 {
		return false
	}
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// freePort best-effort kills whatever holds the given TCP port, so a fresh spawn
// binds cleanly instead of crash-looping behind — or piling a duplicate on top
// of — an orphan from a prior run. Scoped to the EXACT port, so it can never
// touch another service (e.g. the MCS worker on its own port).
func freePort(port int) {
	if port <= 0 {
		return
	}
	_ = exec.Command("fuser", "-k", fmt.Sprintf("%d/tcp", port)).Run()
	time.Sleep(400 * time.Millisecond) // let the socket release before we bind
}

// healthLoop polls registered services every 10 seconds and marks dead
// ones as crashed. Only checks processes we started ourselves.
func (s *Service) healthLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopPoll:
			return
		case <-ticker.C:
			s.reapDead()
		}
	}
}

// reapDead checks all registered services. Dead ones flagged AutoRestart are
// respawned; the rest are marked crashed. This is what keeps a plugin's backing
// host (e.g. webreader) up on its own — the reason the reader stops silently
// today is that nothing brought its process back.
func (s *Service) reapDead() {
	s.mu.Lock()
	recs, err := s.loadRegistry()
	if err != nil || len(recs) == 0 {
		s.mu.Unlock()
		return
	}
	changed := false
	var revive []ServiceRecord
	for i := range recs {
		if recs[i].Status != "running" {
			continue
		}
		if isAlive(recs[i].PID) {
			s.crashes[recs[i].Name] = 0 // healthy — clear the fast-crash counter
			continue
		}
		if !recs[i].AutoRestart {
			log.Printf("[service] %s (pid %d) detected dead, marking crashed", recs[i].Name, recs[i].PID)
			recs[i].Status = "crashed"
			changed = true
			continue
		}
		// Auto-restart candidate. If the port is ALREADY served, an orphan from a
		// prior run holds it — spawning another just crash-loops on a bind conflict
		// (the exact loop this fixes). Leave it: the service is effectively up via
		// whatever answers the port.
		if recs[i].Port > 0 && portOpen(recs[i].Port) {
			log.Printf("[service] %s pid %d dead but port %d still served — not restarting (avoids bind-conflict loop)", recs[i].Name, recs[i].PID, recs[i].Port)
			continue
		}
		// Genuinely down. Back off if it keeps dying right after starting — a
		// service that can't start (bad command, missing dep) must not loop forever.
		if time.Since(recs[i].StartedAt) < minUptime {
			s.crashes[recs[i].Name]++
			if s.crashes[recs[i].Name] >= maxFastCrashes {
				log.Printf("[service] %s crashed %d× within %s of starting — giving up auto-restart", recs[i].Name, s.crashes[recs[i].Name], minUptime)
				recs[i].Status = "crashed"
				recs[i].AutoRestart = false
				changed = true
				continue
			}
		} else {
			s.crashes[recs[i].Name] = 0 // ran healthily for a while before dying
		}
		// Leave the record in place; start() below replaces it. Restart happens
		// after we drop the lock (start() touches the registry).
		revive = append(revive, recs[i])
	}
	if changed {
		s.saveRegistry(recs)
	}
	s.mu.Unlock()

	for _, r := range revive {
		log.Printf("[service] %s (pid %d) died — auto-restarting", r.Name, r.PID)
		if _, err := s.start(map[string]any{
			"name": r.Name, "command": r.Command, "workdir": r.Workdir,
			"port": float64(r.Port), "auto_restart": true,
		}); err != nil {
			log.Printf("[service] %s auto-restart failed: %v", r.Name, err)
		}
	}
}

// StartManaged starts (or restarts) a supervised, auto-restarting service. Used
// by plugin_enable to bring a plugin's backing host up through the SAME path as
// the service tool — tracked, logged, health-checked, respawned on crash — so a
// plugin host is never an unmanaged orphan again.
func (s *Service) StartManaged(name, command, workdir string, port int) error {
	_, err := s.start(map[string]any{
		"name": name, "command": command, "workdir": workdir,
		"port": float64(port), "auto_restart": true,
	})
	return err
}

// StopPolling shuts down the background health checker. Call on agent shutdown.
func (s *Service) StopPolling() {
	close(s.stopPoll)
}

func (s *Service) Name() string { return "service" }

func (s *Service) Description() string {
	return "Manage long-running processes (servers, daemons, dev servers, watchers). " +
		"Actions: start, stop, restart, status, logs, list, remove. " +
		"Use this INSTEAD of bash for any process that doesn't terminate quickly — " +
		"bash blocks waiting for the command to exit, which servers never do. " +
		"The service tool spawns in a detached session, tracks the PID, writes " +
		"stdout/stderr to log files, and returns immediately."
}

func (s *Service) Impact(params map[string]any) int {
	action, _ := params["action"].(string)
	switch action {
	case "logs", "status", "list":
		return toolapi.ImpactObserve
	}
	return toolapi.ImpactAffect
}

var serviceParamSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"action":  {"type": "string", "enum": ["start","stop","restart","status","logs","list","remove"], "description": "Action to perform"},
		"name":    {"type": "string", "description": "Service name — required for all actions except list"},
		"command": {"type": "string", "description": "Shell command to run — required for start"},
		"workdir": {"type": "string", "description": "Working directory for the command (optional, defaults to workspace)"},
		"port":    {"type": "integer", "description": "Port the service listens on — used for health checks (optional)"},
		"auto_restart": {"type": "boolean", "description": "Respawn this service automatically if it crashes (optional, for start)"},
		"lines":   {"type": "integer", "description": "Number of log lines to return (default 50, for logs action)"},
		"stream":  {"type": "string", "enum": ["out","err","both"], "description": "Which log stream to tail (default both, for logs action)"}
	},
	"required": ["action"]
}`)

func (s *Service) Parameters() json.RawMessage { return serviceParamSchema }

// ServiceRecord is one entry in services.json.
type ServiceRecord struct {
	Name        string    `json:"name"`
	Command     string    `json:"command"`
	Workdir     string    `json:"workdir"`
	Port        int       `json:"port,omitempty"`
	PID         int       `json:"pid"`
	StartedAt   time.Time `json:"started_at"`
	Status      string    `json:"status"` // running | stopped | crashed
	LogOut      string    `json:"log_out"`
	LogErr      string    `json:"log_err"`
	AutoRestart bool      `json:"auto_restart,omitempty"` // health loop respawns this on crash
}

// Execute satisfies the Tool interface for callers outside the DAG.
func (s *Service) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(s.ExecuteTyped(ctx, params))
}

func (s *Service) ExecuteTyped(_ context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	action, _ := params["action"].(string)
	switch action {
	case "start":
		return s.start(params)
	case "stop":
		return s.stop(params)
	case "restart":
		return s.restart(params)
	case "status":
		return s.status(params)
	case "logs":
		return s.logs(params)
	case "list":
		return s.list()
	case "remove":
		return s.remove(params)
	case "":
		return toolapi.ToolMessage{}, fmt.Errorf("service: action is required (start/stop/restart/status/logs/list/remove)")
	default:
		return toolapi.ToolMessage{}, fmt.Errorf("service: unknown action %q", action)
	}
}

// ── Registry ──

func (s *Service) registryPath() string {
	return filepath.Join(s.workspace, ".services.json")
}

func (s *Service) logsDir() string {
	return filepath.Join(s.workspace, ".services")
}

func (s *Service) loadRegistry() ([]ServiceRecord, error) {
	data, err := os.ReadFile(s.registryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var recs []ServiceRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("parse services.json: %w", err)
	}
	return recs, nil
}

func (s *Service) saveRegistry(recs []ServiceRecord) error {
	if err := os.MkdirAll(s.workspace, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write via temp file + rename
	tmp := s.registryPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.registryPath())
}

// findRecord returns the record matching name, the full registry slice, and the index.
// Index is -1 if not found.
func (s *Service) findRecord(name string) (*ServiceRecord, []ServiceRecord, int, error) {
	recs, err := s.loadRegistry()
	if err != nil {
		return nil, nil, -1, err
	}
	for i := range recs {
		if recs[i].Name == name {
			return &recs[i], recs, i, nil
		}
	}
	return nil, recs, -1, nil
}

// ── Process helpers ──

// isAlive returns true if the PID exists, is owned by us, and is not a
// zombie. Uses signal 0 for existence check, then reads /proc/<pid>/status
// to detect zombies (State: Z). Zombies still have a pid entry but are
// dead — the service tool must not treat them as running.
func isAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	// Check for zombie state on Linux
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		// Can't read proc — assume alive if signal 0 passed
		return true
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "State:") {
			return !strings.Contains(line, "Z (zombie)")
		}
	}
	return true
}

// killGracefully sends SIGTERM, waits up to timeout, then SIGKILL if still alive.
func killGracefully(pid int, timeout time.Duration) error {
	if !isAlive(pid) {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("sigterm: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !isAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

// ── Actions ──

func (s *Service) start(params map[string]any) (toolapi.ToolMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name, _ := params["name"].(string)
	command, _ := params["command"].(string)
	workdir, _ := params["workdir"].(string)
	port, _ := toolapi.ParamNum(params, "port")
	autoRestart, _ := params["auto_restart"].(bool)
	if name == "" {
		return toolapi.ToolMessage{}, fmt.Errorf("start: name is required")
	}
	if command == "" {
		return toolapi.ToolMessage{}, fmt.Errorf("start: command is required")
	}
	if workdir == "" {
		workdir = s.workspace
	} else if !filepath.IsAbs(workdir) {
		workdir = filepath.Join(s.workspace, workdir)
	}

	// Strip a redundant leading `cd <workdir>` when the planner set both
	// workdir and an inline cd to the same place. The shell would resolve
	// the cd RELATIVE to workdir (which IS workdir), looking for a
	// nested duplicate that doesn't exist — `sh: cd: can't cd to project`.
	if base := filepath.Base(workdir); base != "" && base != "." && base != "/" {
		trimmed := strings.TrimSpace(command)
		for _, prefix := range []string{"cd " + workdir, "cd " + base, "cd ./" + base} {
			rest, ok := strings.CutPrefix(trimmed, prefix)
			if !ok {
				continue
			}
			rest = strings.TrimLeft(rest, " ")
			if strings.HasPrefix(rest, "&&") || strings.HasPrefix(rest, ";") {
				rest = strings.TrimLeft(strings.TrimLeft(rest, "&;"), " ")
				log.Printf("[service] %s: stripped redundant %q (workdir already %s)", name, prefix, workdir)
				command = rest
				break
			}
		}
	}

	existing, recs, idx, err := s.findRecord(name)
	if err != nil {
		return toolapi.ToolMessage{}, err
	}
	if existing != nil {
		if isAlive(existing.PID) {
			// Check if command changed — if so, restart with new command
			if existing.Command == command {
				return toolapi.ToolOK("service", "", map[string]any{
					"status":  "already_running",
					"name":    existing.Name,
					"pid":     existing.PID,
					"message": fmt.Sprintf("service %q already running (pid %d)", name, existing.PID),
				}), nil
			}
			// Command changed — kill old process and start fresh
			log.Printf("[service] %s command changed, restarting (old pid %d)", name, existing.PID)
			killGracefully(existing.PID, 3*time.Second)
		} else {
			// Process is dead/zombie — clean up and proceed to start
			log.Printf("[service] %s pid %d is dead, restarting", name, existing.PID)
		}
		// Remove stale record — the start logic below will create a new one
		recs = append(recs[:idx], recs[idx+1:]...)
		idx = -1 // invalidate — new record will be appended
		s.saveRegistry(recs)
	}

	// Ensure logs directory exists
	if err := os.MkdirAll(s.logsDir(), 0755); err != nil {
		return toolapi.ToolMessage{}, fmt.Errorf("create logs dir: %w", err)
	}
	logOut := filepath.Join(s.logsDir(), name+".out.log")
	logErr := filepath.Join(s.logsDir(), name+".err.log")

	// Truncate logs on every start. Each `service start` is semantically a
	// fresh run (the old process was killed above), and validators tail these
	// files looking for current-run errors. Appending across runs leaves stale
	// pre-fix errors in place and traps the debugger in infinite "still broken"
	// loops on a problem that was already resolved.
	outFile, err := os.OpenFile(logOut, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return toolapi.ToolMessage{}, fmt.Errorf("open stdout log: %w", err)
	}
	defer outFile.Close()
	errFile, err := os.OpenFile(logErr, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return toolapi.ToolMessage{}, fmt.Errorf("open stderr log: %w", err)
	}
	defer errFile.Close()

	// Clear the target port of any stray listener before spawning. Without this,
	// an orphan from a prior run (a detached process that outlived a kaiju restart)
	// keeps the port, so the fresh process fails to bind and either crash-loops or
	// piles up as a duplicate — the exact uvicorn-multiplication this prevents.
	// One process can ever hold the port, so there is only ever one instance.
	freePort(int(port))

	// Spawn in a detached session so the child outlives kaiju.
	// Setsid makes the child its own session leader.
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = workdir
	cmd.Stdout = outFile
	cmd.Stderr = errFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return toolapi.ToolMessage{}, fmt.Errorf("start process: %w", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release() // don't hold a reaper reference

	record := ServiceRecord{
		Name:        name,
		Command:     command,
		Workdir:     workdir,
		Port:        int(port),
		PID:         pid,
		StartedAt:   time.Now().UTC(),
		Status:      "running",
		LogOut:      logOut,
		LogErr:      logErr,
		AutoRestart: autoRestart,
	}

	if idx >= 0 {
		recs[idx] = record
	} else {
		recs = append(recs, record)
	}
	if err := s.saveRegistry(recs); err != nil {
		return toolapi.ToolMessage{}, fmt.Errorf("save registry: %w", err)
	}

	result := map[string]any{
		"status":  "started",
		"name":    name,
		"pid":     pid,
		"log_out": logOut,
		"log_err": logErr,
	}
	if port > 0 {
		result["port"] = int(port)
	}
	return toolapi.ToolOK("service", "", result), nil
}

func (s *Service) stop(params map[string]any) (toolapi.ToolMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name, _ := params["name"].(string)
	if name == "" {
		return toolapi.ToolMessage{}, fmt.Errorf("stop: name is required")
	}

	rec, recs, idx, err := s.findRecord(name)
	if err != nil {
		return toolapi.ToolMessage{}, err
	}
	if rec == nil {
		return toolapi.ToolMessage{}, fmt.Errorf("stop: service %q not found", name)
	}

	if !isAlive(rec.PID) {
		recs[idx].Status = "stopped"
		_ = s.saveRegistry(recs)
		return toolapi.ToolOK("service", "", map[string]any{
			"status": "already_stopped",
			"name":   name,
		}), nil
	}

	if err := killGracefully(rec.PID, 5*time.Second); err != nil {
		return toolapi.ToolMessage{}, fmt.Errorf("kill process: %w", err)
	}
	recs[idx].Status = "stopped"
	if err := s.saveRegistry(recs); err != nil {
		return toolapi.ToolMessage{}, err
	}
	return toolapi.ToolOK("service", "", map[string]any{
		"status": "stopped",
		"name":   name,
	}), nil
}

func (s *Service) restart(params map[string]any) (toolapi.ToolMessage, error) {
	name, _ := params["name"].(string)
	if name == "" {
		return toolapi.ToolMessage{}, fmt.Errorf("restart: name is required")
	}

	rec, _, _, err := s.findRecord(name)
	if err != nil {
		return toolapi.ToolMessage{}, err
	}
	if rec == nil {
		return toolapi.ToolMessage{}, fmt.Errorf("restart: service %q not found", name)
	}

	if _, err := s.stop(map[string]any{"name": name}); err != nil {
		return toolapi.ToolMessage{}, err
	}
	startParams := map[string]any{
		"name":    name,
		"command": rec.Command,
		"workdir": rec.Workdir,
	}
	if rec.Port > 0 {
		startParams["port"] = float64(rec.Port)
	}
	return s.start(startParams)
}

func (s *Service) status(params map[string]any) (toolapi.ToolMessage, error) {
	name, _ := params["name"].(string)
	if name == "" {
		return toolapi.ToolMessage{}, fmt.Errorf("status: name is required")
	}
	rec, _, _, err := s.findRecord(name)
	if err != nil {
		return toolapi.ToolMessage{}, err
	}
	if rec == nil {
		return toolapi.ToolMessage{}, fmt.Errorf("status: service %q not found", name)
	}
	alive := isAlive(rec.PID)
	status := rec.Status
	if status == "running" && !alive {
		status = "crashed"
	}
	return toolapi.ToolOK("service", "", map[string]any{
		"name":       rec.Name,
		"status":     status,
		"pid":        rec.PID,
		"alive":      alive,
		"command":    rec.Command,
		"workdir":    rec.Workdir,
		"started_at": rec.StartedAt.Format(time.RFC3339),
		"uptime_sec": int(time.Since(rec.StartedAt).Seconds()),
		"log_out":    rec.LogOut,
		"log_err":    rec.LogErr,
	}), nil
}

func (s *Service) logs(params map[string]any) (toolapi.ToolMessage, error) {
	name, _ := params["name"].(string)
	if name == "" {
		return toolapi.ToolMessage{}, fmt.Errorf("logs: name is required")
	}
	linesNum := 50
	if v, ok := toolapi.ParamNum(params, "lines"); ok {
		linesNum = int(v)
	}
	if v, ok := params["lines"].(int); ok {
		linesNum = v
	}
	if linesNum <= 0 {
		linesNum = 50
	}
	stream, _ := params["stream"].(string)
	if stream == "" {
		stream = "both"
	}

	rec, _, _, err := s.findRecord(name)
	if err != nil {
		return toolapi.ToolMessage{}, err
	}
	if rec == nil {
		return toolapi.ToolMessage{}, fmt.Errorf("logs: service %q not found", name)
	}

	result := map[string]any{"name": name}
	if stream == "out" || stream == "both" {
		result["stdout"] = tailFile(rec.LogOut, linesNum)
	}
	if stream == "err" || stream == "both" {
		result["stderr"] = tailFile(rec.LogErr, linesNum)
	}
	return toolapi.ToolOK("service", "", result), nil
}

func (s *Service) list() (toolapi.ToolMessage, error) {
	recs, err := s.loadRegistry()
	if err != nil {
		return toolapi.ToolMessage{}, err
	}
	out := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		alive := isAlive(rec.PID)
		status := rec.Status
		if status == "running" && !alive {
			status = "crashed"
		}
		out = append(out, map[string]any{
			"name":       rec.Name,
			"status":     status,
			"pid":        rec.PID,
			"alive":      alive,
			"command":    rec.Command,
			"started_at": rec.StartedAt.Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["name"].(string) < out[j]["name"].(string)
	})
	return toolapi.ToolOK("service", "", map[string]any{"services": out, "count": len(out)}), nil
}

func (s *Service) remove(params map[string]any) (toolapi.ToolMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name, _ := params["name"].(string)
	if name == "" {
		return toolapi.ToolMessage{}, fmt.Errorf("remove: name is required")
	}
	rec, recs, idx, err := s.findRecord(name)
	if err != nil {
		return toolapi.ToolMessage{}, err
	}
	if rec == nil {
		return toolapi.ToolMessage{}, fmt.Errorf("remove: service %q not found", name)
	}
	if isAlive(rec.PID) {
		return toolapi.ToolMessage{}, fmt.Errorf("remove: service %q is still running (stop it first)", name)
	}
	recs = append(recs[:idx], recs[idx+1:]...)
	if err := s.saveRegistry(recs); err != nil {
		return toolapi.ToolMessage{}, err
	}
	return toolapi.ToolOK("service", "", map[string]any{
		"status": "removed",
		"name":   name,
	}), nil
}

// ── Helpers ──

// tailFile reads a log file and returns the last n lines. Simple approach:
// read whole file, split, take last n. Fine for dev/typical servers; if a
// user has gigabyte log files we'll need a smarter implementation.
func tailFile(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		return fmt.Sprintf("error reading log: %v", err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Sprintf("error reading log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// OutputSchema declares the uniform tool envelope; the service payload is
// action-specific and carried in data.
func (s *Service) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema("")
}

func toJSON(v any) string {
	// Every service action returns through here — wrap the payload in the uniform
	// tool envelope. The action-specific fields (status, name, pid, services, …)
	// ride in Data, so field access and the scheduler's health-check graft read
	// them from there.
	return toolapi.ToolOK("service", "", v).JSON()
}
