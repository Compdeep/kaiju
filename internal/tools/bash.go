package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/user/kaiju/internal/agent/tools"
)

/*
 * destructivePattern matches commands that are likely destructive.
 * desc: Regex pattern for dangerous shell commands like rm -rf, mkfs, kill, shutdown, etc.
 */
var destructivePattern = regexp.MustCompile(`(?i)\b(rm\s+-rf|rm\s+-r|rmdir|del\s+/|rd\s+/s|format\s+|mkfs|dd\s+if=|kill\s+-9|killall|pkill|shutdown|reboot|halt|init\s+[06]|systemctl\s+(stop|disable|mask)|chmod\s+-R|chown\s+-R)\b`)

/*
 * writePattern matches commands that write to disk but aren't destructive.
 * desc: Regex pattern for commands that modify files or install packages without being destructive.
 */
var writePattern = regexp.MustCompile(`(?i)(>\s*\S|>>|tee\s|cp\s|mv\s|mkdir|touch|wget\s|curl\s.*-o|apt\s+install|yum\s+install|pip\s+install|npm\s+install|go\s+install)`)

/*
 * Bash executes shell commands with dynamic impact based on command content.
 * desc: Tool that runs arbitrary shell commands via sh, powershell, or cmd with configurable timeout.
 */
type Bash struct {
	shell     string
	timeout   time.Duration
	workDir   string
}

/*
 * NewBash creates a new Bash tool configured with the given shell.
 * desc: Initializes Bash with shell auto-detection for the current OS if shell is empty or "auto".
 * param: shell - shell interpreter to use ("sh", "powershell", "cmd", or "auto" for OS default)
 * return: configured Bash tool instance
 */
func NewBash(shell string, workDir ...string) *Bash {
	if shell == "" || shell == "auto" {
		if runtime.GOOS == "windows" {
			shell = "powershell"
		} else {
			shell = "sh"
		}
	}
	wd := ""
	if len(workDir) > 0 && workDir[0] != "" {
		wd = workDir[0]
	}
	return &Bash{shell: shell, timeout: 60 * time.Second, workDir: wd}
}

/*
 * Name returns the tool identifier.
 * desc: Returns "bash" as the tool name.
 * return: the string "bash"
 */
func (b *Bash) Name() string { return "bash" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains that bash is a general-purpose command execution tool.
 * return: description string
 */
func (b *Bash) Description() string {
	return "Execute any command, script, or program available on the system. This is the general-purpose tool — if something can be done from the command line, use bash. Covers: running CLI tools, downloading files, processing data, managing packages, automation, and anything else the OS can do."
}

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing stdout and stderr from the command.
 * return: JSON schema as raw bytes
 */
func (b *Bash) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"output":{"type":"string","description":"stdout + stderr from the command"}}}`)
}

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines the command string and optional timeout_sec parameters.
 * return: JSON schema as raw bytes
 */
func (b *Bash) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {
				"type": "string",
				"description": "The shell command to execute"
			},
			"timeout_sec": {
				"type": "integer",
				"description": "Timeout in seconds (default 60, max 300)"
			}
		},
		"required": ["command"],
		"additionalProperties": false
	}`)
}

/*
 * Impact analyzes the command string to determine its safety level.
 * desc: Classifies the command into one of three impact tiers (0/1/2) via
 *       regex pattern matching. The registry maps these tiers to ranks.
 * param: params - tool parameters containing the "command" string to analyze
 * return: impact tier 0, 1, or 2
 */
func (b *Bash) Impact(params map[string]any) int {
	cmd, _ := params["command"].(string)
	if cmd == "" {
		cmd, _ = params["cmd"].(string)
	}
	if cmd == "" {
		return tools.ImpactObserve
	}
	if destructivePattern.MatchString(cmd) {
		return tools.ImpactControl
	}
	if writePattern.MatchString(cmd) {
		return tools.ImpactAffect
	}
	return tools.ImpactObserve
}

/*
 * Execute runs the shell command and returns combined stdout/stderr output.
 * desc: Executes the command using the configured shell with timeout, truncating output to 8KB.
 * param: ctx - context for cancellation
 * param: params - must contain "command" (or "cmd" alias); optionally "timeout_sec"
 * return: combined stdout/stderr output (truncated to 8KB), or error on timeout/failure
 */
func (b *Bash) Execute(ctx context.Context, params map[string]any) (string, error) {
	command, _ := params["command"].(string)
	// Accept "cmd" as alias — LLMs frequently hallucinate this param name
	if command == "" {
		command, _ = params["cmd"].(string)
	}
	if command == "" {
		return "", fmt.Errorf("bash: command is required")
	}

	timeout := b.timeout
	if ts, ok := params["timeout_sec"].(float64); ok && ts > 0 {
		if ts > 300 {
			ts = 300
		}
		timeout = time.Duration(ts) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	switch b.shell {
	case "powershell":
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", command)
	case "cmd":
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	default:
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}

	if b.workDir != "" {
		cmd.Dir = b.workDir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	var result strings.Builder
	if stdout.Len() > 0 {
		result.WriteString(stdout.String())
	}
	if stderr.Len() > 0 {
		if result.Len() > 0 {
			result.WriteString("\n--- stderr ---\n")
		}
		result.WriteString(stderr.String())
	}

	// Truncate to 8KB
	output := result.String()
	if len(output) > 8192 {
		output = output[:8192] + "\n... (truncated)"
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return output, fmt.Errorf("bash: command timed out after %s", timeout)
		}
		// Return structured error as result (nil error so node resolves).
		// The scheduler detects execute node failures from the result content.
		exitCode := -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		stdoutStr := stdout.String()
		stderrStr := stderr.String()
		if len(stdoutStr) > 200 {
			stdoutStr = stdoutStr[:200] + "..."
		}
		if len(stderrStr) > 200 {
			stderrStr = stderrStr[:200] + "..."
		}
		errInfo := map[string]any{
			"bash_error": true,
			"exit_code":  exitCode,
			"stdout":     stdoutStr,
			"stderr":     stderrStr,
			"error":      err.Error(),
			"command":    command,
		}
		errJSON, _ := json.Marshal(errInfo)
		return string(errJSON), nil
	}

	return output, nil
}

var _ tools.Tool = (*Bash)(nil)
