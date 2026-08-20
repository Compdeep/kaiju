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

// freePort clears the given TCP port of a process THIS TOOL started, so a fresh
// spawn binds cleanly instead of crash-looping behind — or piling a duplicate on
// top of — an orphan from a prior run.
//
// It used to run `fuser -k <port>/tcp`, which kills whatever holds the port. On
// a machine that is only running the agent, that is the orphan. On a machine
// somebody else is using, it is whatever they had bound there — a database, an
// editor's language server, their own application — killed without a word by a
// tool asked to start something unrelated.
//
// So the port is not the identity. The registry is: it records the pid and
// command of every process this tool spawned, against the port it was given. A
// port held by something not in that registry is somebody else's and is left
// alone, and the spawn that follows fails to bind — which is the honest
// outcome, and is logged so the reason is visible.
func (s *Service) freePort(port int) {
	if port <= 0 {
		return
	}
	recs, err := s.loadRegistry()
	if err != nil {
		log.Printf("[service] port %d: cannot read the registry, leaving it alone: %v", port, err)
		return
	}
	killed := false
	for _, r := range recs {
		if r.Port != port || r.PID <= 0 || !processIsAlive(r.PID) {
			continue
		}
		// A pid outlives the process that owned it, and the registry survives a
		// reboot, so a recorded pid can belong to a stranger by now. Signalling
		// the group without checking would kill that stranger and its children.
		if !pidRunsCommand(r.PID, r.Command) {
			log.Printf("[service] port %d: pid %d is no longer %q, leaving it alone", port, r.PID, r.Name)
			continue
		}
		log.Printf("[service] port %d: clearing our own %q (pid %d)", port, r.Name, r.PID)
		_ = killGracefully(r.PID, 5*time.Second)
		killed = true
	}
	if !killed {
		if portOpen(port) {
			log.Printf("[service] port %d is held by a process this tool did not start; "+
				"leaving it alone, so the spawn below will fail to bind", port)
		}
		return
	}
	time.Sleep(400 * time.Millisecond) // let the socket release before we bind
}

/*
 * pidRunsCommand reports whether a live pid is running the recorded command.
 * desc: Reads the process's own argument vector. start runs the command through
 *       `sh -c`, so the recorded command appears there verbatim. Fails closed —
 *       a pid whose arguments cannot be read is treated as not ours, because
 *       the cost of being wrong is killing a process that belongs to someone
 *       else.
 * param: pid - the process to identify.
 * param: command - the command the registry recorded for it.
 * return: true only when this is demonstrably the recorded process.
 */
func pidRunsCommand(pid int, command string) bool {
	if pid <= 0 || strings.TrimSpace(command) == "" {
		return false
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	// The argument vector is NUL-separated; join it back before matching.
	return strings.Contains(strings.ReplaceAll(string(raw), "\x00", " "), command)
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
		if processIsAlive(recs[i].PID) {
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
	"required": ["action"],
	"allOf": [
		{"if": {"properties": {"action": {"enum": ["start", "stop", "restart", "status", "logs", "remove"]}}, "required": ["action"]},
		 "then": {"required": ["name"]}},
		{"if": {"properties": {"action": {"const": "start"}}, "required": ["action"]},
		 "then": {"required": ["command"]}}
	]
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

// killGracefully sends SIGTERM to the service, waits up to timeout, then
// SIGKILL to anything still there.
//
// To the process group, not the pid. start runs the command through `sh -c` in
// a session of its own, so the pid on the record is the shell's and the command
// is its child. Signalling the pid alone killed the shell and left the command
// running: reparented to init, still bound to whatever port it had, and no
// longer named in any registry. stop said "stopped" and the service was up.
// Those are the orphans freePort exists to clear, and this is where they came
// from.
//
// Waiting on the group rather than the shell matters for the same reason. The
// shell exits at once; a child that is slow to shut down, or ignores SIGTERM,
// is what the timeout is for.
func killGracefully(pid int, timeout time.Duration) error {
	if pid <= 1 {
		return nil
	}
	if !processIsAlive(pid) && !treeIsAlive(pid) {
		return nil
	}
	if err := stopProcessTree(pid); err != nil {
		return fmt.Errorf("asking the process tree to stop: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		reapProcess(pid)
		if !treeIsAlive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return killProcessTree(pid)
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
		if processIsAlive(existing.PID) {
			// Check if command changed — if so, restart with new command
			if existing.Command == command {
				return toolapi.ToolOK("service",
					fmt.Sprintf("%s is already running (pid %d)", existing.Name, existing.PID),
					map[string]any{
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
	s.freePort(int(port))

	// Spawn in a detached session so the child outlives kaiju.
	// Setsid makes the child its own session leader.
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = workdir
	cmd.Stdout = outFile
	cmd.Stderr = errFile
	setOwnSession(cmd)
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
	text := fmt.Sprintf("started %s (pid %d)", name, pid)
	if port > 0 {
		text += fmt.Sprintf(" on port %d", int(port))
	}
	return toolapi.ToolOK("service", text, result), nil
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
		return toolapi.ToolEmpty("service", fmt.Sprintf(
			"no service named %q is registered here, so there was nothing to stop", name)), nil
	}

	if !processIsAlive(rec.PID) {
		recs[idx].Status = "stopped"
		_ = s.saveRegistry(recs)
		return toolapi.ToolOK("service", name+" was already stopped", map[string]any{
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
	return toolapi.ToolOK("service", "stopped "+name, map[string]any{
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
		return toolapi.ToolEmpty("service", fmt.Sprintf(
			"no service named %q is registered here, so there was nothing to restart", name)), nil
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
		// Not registered is an answer, and asking is how a caller finds out. A Go
		// error ends the step and takes the answer with it.
		return toolapi.ToolEmpty("service", fmt.Sprintf(
			"no service named %q is registered here", name)), nil
	}
	alive := processIsAlive(rec.PID)
	status := rec.Status
	if status == "running" && !alive {
		status = "crashed"
	}
	return toolapi.ToolOK("service",
		fmt.Sprintf("%s is %s (pid %d, up %ds): %s",
			rec.Name, status, rec.PID, int(time.Since(rec.StartedAt).Seconds()), rec.Command),
		map[string]any{
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
		// Not registered is an answer, and asking is how a caller finds out. A Go
		// error ends the step and takes the answer with it.
		return toolapi.ToolEmpty("service", fmt.Sprintf(
			"no service named %q is registered here", name)), nil
	}

	result := map[string]any{"name": name}
	var text strings.Builder
	if stream == "out" || stream == "both" {
		out := tailFile(rec.LogOut, linesNum)
		result["stdout"] = out
		text.WriteString(out)
	}
	if stream == "err" || stream == "both" {
		errOut := tailFile(rec.LogErr, linesNum)
		result["stderr"] = errOut
		if text.Len() > 0 && errOut != "" {
			text.WriteString("\n--- standard error ---\n")
		}
		text.WriteString(errOut)
	}
	if strings.TrimSpace(text.String()) == "" {
		return toolapi.ToolEmpty("service", name+" has written nothing to its logs"), nil
	}
	return toolapi.ToolOK("service", text.String(), result), nil
}

func (s *Service) list() (toolapi.ToolMessage, error) {
	recs, err := s.loadRegistry()
	if err != nil {
		return toolapi.ToolMessage{}, err
	}
	out := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		alive := processIsAlive(rec.PID)
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
	if len(out) == 0 {
		return toolapi.ToolEmpty("service", "no service is registered"), nil
	}
	var listing strings.Builder
	for _, rec := range out {
		listing.WriteString(fmt.Sprintf("%s %s pid=%v alive=%v %s\n",
			rec["name"], rec["status"], rec["pid"], rec["alive"], rec["command"]))
	}
	return toolapi.ToolOK("service", strings.TrimRight(listing.String(), "\n"),
		map[string]any{"services": out, "count": len(out)}), nil
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
		return toolapi.ToolEmpty("service", fmt.Sprintf(
			"no service named %q is registered here, so there was nothing to remove", name)), nil
	}
	if processIsAlive(rec.PID) {
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
// OutputSchema declares the keys this tool's payload carries, per action.
//
// It declared none of them, so a run that started a service could not be followed
// by one naming its pid, and a planner reading the declaration was told only that
// there is an envelope.
//
// Written out here rather than derived from a struct, unlike the other tools in
// this package. The payloads are built as maps and stay maps: a struct with
// omitempty would drop alive when it is false and pid when it is 0, and a struct
// without it would add every key to every action. Either changes the JSON an
// existing reader receives. The contract test holds this list to the source by
// requiring each name to appear in this file, which is where the maps are.
func (s *Service) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(`{"type":"object",` +
		`"description":"Which keys are present depends on the action. status, name: every action. services, count: list. stdout, stderr: logs. uptime_sec, alive, workdir: status.",` +
		`"properties":{` +
		`"status":{"type":"string","description":"started, already_running, stopped, already_stopped, running or crashed"},` +
		`"name":{"type":"string","description":"the service"},` +
		`"pid":{"type":"integer","description":"the process running it"},` +
		`"alive":{"type":"boolean","description":"whether that process exists now, which is how a crashed service is told from a running one"},` +
		`"command":{"type":"string","description":"the command line it was started with"},` +
		`"workdir":{"type":"string","description":"the directory it runs in"},` +
		`"port":{"type":"integer","description":"the port it was started on, when one was given"},` +
		`"started_at":{"type":"string","format":"date-time","description":"when it was started"},` +
		`"uptime_sec":{"type":"integer","description":"seconds since it was started"},` +
		`"log_out":{"type":"string","description":"path of the file its standard output is written to"},` +
		`"log_err":{"type":"string","description":"path of the file its standard error is written to"},` +
		`"stdout":{"type":"string","description":"action=logs: the last lines of standard output"},` +
		`"stderr":{"type":"string","description":"action=logs: the last lines of standard error"},` +
		`"message":{"type":"string","description":"why nothing was done, when nothing was done"},` +
		`"count":{"type":"integer","description":"action=list: how many services are registered"},` +
		`"services":{"type":"array","description":"action=list: one per registered service",` +
		`"items":{"type":"object","properties":{` +
		`"name":{"type":"string"},"status":{"type":"string"},"pid":{"type":"integer"},` +
		`"alive":{"type":"boolean"},"command":{"type":"string"},"started_at":{"type":"string","format":"date-time"}}}}` +
		`}}`)
}
