package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
)

// ─── ProcessList ────────────────────────────────────────────────────────────

/*
 * ProcessList lists running processes with optional filtering.
 * desc: Tool that queries the OS for running processes and returns a formatted table with PID, name, CPU, and memory.
 */
type ProcessList struct{}

/*
 * NewProcessList creates a new ProcessList tool instance.
 * desc: Returns a zero-value ProcessList ready for use.
 * return: pointer to a new ProcessList
 */
func NewProcessList() *ProcessList { return &ProcessList{} }

/*
 * Name returns the tool identifier.
 * desc: Returns "process_list" as the tool name.
 * return: the string "process_list"
 */
func (p *ProcessList) Name() string { return "process_list" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains that this tool lists running processes with optional name filtering.
 * return: description string
 */
func (p *ProcessList) Description() string {
	return "List running processes with PID, name, CPU and memory usage. Optionally filter by name."
}

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always returns ImpactObserve since listing processes is non-destructive.
 * param: _ - unused parameters
 * return: ImpactObserve (0)
 */
func (p *ProcessList) Impact(map[string]any) int { return agenttools.ImpactObserve }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing a process list table as a string.
 * return: JSON schema as raw bytes
 */
func (p *ProcessList) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"output":{"type":"string","description":"process list table"}}}`)
}

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines the optional filter (substring match) and limit (default 30) parameters.
 * return: JSON schema as raw bytes
 */
func (p *ProcessList) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filter": {"type": "string", "description": "Filter processes by name (substring match)"},
			"limit": {"type": "integer", "description": "Max processes to return (default: 30)"}
		},
		"additionalProperties": false
	}`)
}

/*
 * Execute lists running processes, optionally filtered by name.
 * desc: Runs ps (Unix) or Get-Process (Windows), filters by name substring, and limits output count.
 * param: ctx - context for cancellation
 * param: params - optionally contains "filter" and "limit"
 * return: formatted process list table as a string, or error on command failure
 */
func (p *ProcessList) Execute(ctx context.Context, params map[string]any) (string, error) {
	filter, _ := params["filter"].(string)
	limit := 30
	if l, ok := params["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command",
			"Get-Process | Sort-Object CPU -Descending | Select-Object -First "+strconv.Itoa(limit*2)+
				" Id, ProcessName, CPU, @{N='MemMB';E={[math]::Round($_.WorkingSet64/1MB,1)}} | Format-Table -AutoSize")
	default:
		cmd = exec.CommandContext(ctx, "ps", "aux", "--sort=-pcpu")
	}

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("process_list: %w", err)
	}

	lines := strings.Split(string(out), "\n")
	var result []string
	count := 0
	for i, line := range lines {
		if i == 0 {
			result = append(result, line) // header
			continue
		}
		if count >= limit {
			break
		}
		if filter != "" && !strings.Contains(strings.ToLower(line), strings.ToLower(filter)) {
			continue
		}
		if strings.TrimSpace(line) != "" {
			result = append(result, line)
			count++
		}
	}

	return strings.Join(result, "\n"), nil
}

var _ agenttools.Tool = (*ProcessList)(nil)

// ─── ProcessKill ────────────────────────────────────────────────────────────

/*
 * ProcessKill terminates a process by PID.
 * desc: Tool that sends a termination signal (SIGTERM or SIGKILL) to a process, or uses taskkill on Windows.
 */
type ProcessKill struct{}

/*
 * NewProcessKill creates a new ProcessKill tool instance.
 * desc: Returns a zero-value ProcessKill ready for use.
 * return: pointer to a new ProcessKill
 */
func NewProcessKill() *ProcessKill { return &ProcessKill{} }

/*
 * Name returns the tool identifier.
 * desc: Returns "process_kill" as the tool name.
 * return: the string "process_kill"
 */
func (p *ProcessKill) Name() string { return "process_kill" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains that this tool terminates a process by PID.
 * return: description string
 */
func (p *ProcessKill) Description() string {
	return "Terminate a process by PID. Use process_list first to find the target."
}

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always returns ImpactControl since killing processes is a destructive operation.
 * param: _ - unused parameters
 * return: ImpactControl (2)
 */
func (p *ProcessKill) Impact(map[string]any) int { return agenttools.ImpactControl }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing the kill result message.
 * return: JSON schema as raw bytes
 */
func (p *ProcessKill) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"result":{"type":"string","description":"kill result"}}}`)
}

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines the required pid integer and optional force boolean parameters.
 * return: JSON schema as raw bytes
 */
func (p *ProcessKill) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"pid": {"type": "integer", "description": "Process ID to kill"},
			"force": {"type": "boolean", "description": "Force kill (SIGKILL on Unix, /F on Windows). Default: false"}
		},
		"required": ["pid"],
		"additionalProperties": false
	}`)
}

/*
 * Execute terminates the specified process by PID.
 * desc: Sends SIGTERM (or SIGKILL with force) on Unix, or runs taskkill on Windows.
 * param: ctx - context for cancellation
 * param: params - must contain "pid"; optionally "force" for SIGKILL/forced termination
 * return: confirmation message with PID and force status, or error if PID is missing
 */
func (p *ProcessKill) Execute(ctx context.Context, params map[string]any) (string, error) {
	pidFloat, ok := params["pid"].(float64)
	if !ok {
		return "", fmt.Errorf("process_kill: pid is required")
	}
	pid := int(pidFloat)
	if pid <= 1 {
		return "", fmt.Errorf("process_kill: refusing to kill pid %d (unsafe — use process_list to find the correct PID first)", pid)
	}
	force, _ := params["force"].(bool)

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		args := []string{"/PID", strconv.Itoa(pid)}
		if force {
			args = append(args, "/F")
		}
		cmd = exec.CommandContext(ctx, "taskkill", args...)
	default:
		sig := "-TERM"
		if force {
			sig = "-KILL"
		}
		cmd = exec.CommandContext(ctx, "kill", sig, strconv.Itoa(pid))
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("failed: %v\n%s", err, string(out)), nil
	}
	return fmt.Sprintf("killed pid %d (force=%v)\n%s", pid, force, strings.TrimSpace(string(out))), nil
}

var _ agenttools.Tool = (*ProcessKill)(nil)
