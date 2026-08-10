package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Compdeep/kaiju/agent/toolapi"
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
	shell   string
	timeout time.Duration
	workDir string
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
	return "Execute any command, script, or program available on the system. This is the general-purpose tool — if something can be done from the command line, use bash. Covers: running CLI tools, downloading files, processing data, managing packages, automation, and anything else the OS can do. To manage processes (kill PIDs, free ports, signal daemons) prefer the process_list and process_kill tools."
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
				"description": "Seconds of SILENCE before the command is killed — not how long it may run. A download or a build that keeps printing is never killed for taking a while; one that says nothing for this long is treated as stuck. Default 60. Set 0 to allow any amount of silence."
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
		cmd, _ = params["script"].(string)
	}
	// No command means this is not a call being gated — it is the abstract
	// question, "what tier is this tool", asked when deciding whether to offer
	// it at all. A shell's honest answer to that is its worst case. Answering
	// with the cheapest one ships an unrestricted shell enabled by default,
	// because a command that is not there matches no destructive pattern.
	if cmd == "" {
		return toolapi.ImpactControl
	}
	if destructivePattern.MatchString(cmd) {
		// Destructive commands targeting only workspace paths are safe —
		// the workspace is the agent's sandbox. Downgrade to ImpactAffect.
		if b.workDir != "" && b.isWorkspaceOnly(cmd) {
			return toolapi.ImpactAffect
		}
		return toolapi.ImpactControl
	}
	if writePattern.MatchString(cmd) {
		return toolapi.ImpactAffect
	}
	return toolapi.ImpactObserve
}

// isWorkspaceOnly checks if all paths in a destructive command are relative
// (resolved against workDir) or absolute paths inside workDir.
func (b *Bash) isWorkspaceOnly(cmd string) bool {
	// Extract path arguments after rm -rf / rm -r
	parts := strings.Fields(cmd)
	inRm := false
	for _, p := range parts {
		if p == "rm" || p == "rmdir" {
			inRm = true
			continue
		}
		if inRm && strings.HasPrefix(p, "-") {
			continue // flags like -rf
		}
		if inRm {
			// Check each path argument
			if strings.HasPrefix(p, "/") {
				// Absolute path — must be inside workspace
				if !strings.HasPrefix(p, b.workDir) {
					return false
				}
			}
			// Relative paths resolve against workDir — always safe
			// But reject obvious escapes
			if strings.Contains(p, "..") {
				return false
			}
		}
		if p == "&&" || p == ";" || p == "||" {
			inRm = false // new command segment
		}
	}
	return true
}

/*
 * Execute runs the shell command and returns combined stdout/stderr output.
 * desc: Executes the command using the configured shell with timeout, truncating output to 8KB.
 * param: ctx - context for cancellation
 * param: params - must contain "command" (or "cmd" alias); optionally "timeout_sec"
 * return: combined stdout/stderr output (truncated to 8KB), or error on timeout/failure
 */
func (b *Bash) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	command, _ := params["command"].(string)
	// Accept common aliases — LLMs frequently hallucinate param names
	if command == "" {
		command, _ = params["cmd"].(string)
	}
	if command == "" {
		command, _ = params["script"].(string)
	}
	if command == "" {
		return toolapi.ToolMessage{}, fmt.Errorf("bash: command is required")
	}

	// How long the command may be SILENT, not how long it may run.
	//
	// A download or a build is working the whole time it is producing output,
	// and killing it on a wall clock kills the ones that are doing their job.
	// This used to be guessed at by matching the command text for "wget",
	// "npm install", "go build" and a dozen others, and giving those a longer
	// wall clock — a list that is wrong for anything not on it. Measuring
	// whether the command is saying anything needs no list.
	//
	// 0 means never kill for silence. The run's own wall clock still bounds it.
	idle := b.timeout
	if ts, ok := toolapi.ParamNum(params, "timeout_sec"); ok {
		idle = time.Duration(ts) * time.Second
	}

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
	// Put command in its own process group so we can kill the entire tree
	// (including backgrounded children like `npx vite &`) on timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer
	var lastOutput atomic.Int64
	lastOutput.Store(time.Now().UnixNano())
	touch := writerFunc(func(p []byte) (int, error) {
		lastOutput.Store(time.Now().UnixNano())
		return len(p), nil
	})
	cmd.Stdout = io.MultiWriter(&stdout, touch)
	cmd.Stderr = io.MultiWriter(&stderr, touch)

	err := runUntilIdle(ctx, cmd, idle, &lastOutput)
	// If context timed out, kill the entire process group
	if ctx.Err() == context.DeadlineExceeded && cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

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
		// Silence is a different failure from a non-zero exit, and the model has
		// to be able to tell them apart: one means the command is stuck, the
		// other means it ran and said no.
		if strings.HasPrefix(err.Error(), "no output for ") {
			return toolapi.ToolFail("command", fmt.Sprintf("killed after %s with no output — the command produced nothing for that long, so it was treated as stuck", idle),
				map[string]any{"command": command, "idle_timeout": idle.String(),
					"stdout": headTailTruncate(stdout.String(), 200, 600),
					"stderr": headTailTruncate(stderr.String(), 200, 600)}), nil
		}
		if ctx.Err() != nil {
			return toolapi.ToolMessage{}, fmt.Errorf("bash: %w", ctx.Err())
		}
		// Return structured error as result (nil error so node resolves).
		// The scheduler detects execute node failures from the result content.
		exitCode := -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		// Head+tail truncation. The previous head-only strategy stripped the
		// last line of Python tracebacks (the OverflowError/ValueError/etc.
		// message) — leaving Holmes with the unhelpful "Traceback (most
		// recent call last):" header and no error type. For tracebacks and
		// most CLI errors the diagnostic info lives at the END, not the
		// start, so we keep both ends.
		stdoutStr := headTailTruncate(stdout.String(), 200, 600)
		stderrStr := headTailTruncate(stderr.String(), 200, 600)
		return toolapi.ToolFail("command", fmt.Sprintf("exit %d: %s", exitCode, err.Error()), bashData{
			ExitCode: exitCode,
			Stdout:   stdoutStr,
			Stderr:   stderrStr,
			Command:  command,
		}), nil
	}

	// A command that succeeded and printed nothing: grep with no match, find
	// with no file, ls of an empty directory. It is the commonest way a shell
	// says "not there", and reporting it as a result leaves the next step to
	// infer the absence from an empty string — which reads the same as a
	// command whose output was lost.
	if strings.TrimSpace(output) == "" {
		return toolapi.ToolEmpty("command", fmt.Sprintf("%q exited 0 and printed nothing", command)), nil
	}

	return toolapi.ToolOK("command", output, bashData{
		ExitCode: 0,
		Stdout:   headTailTruncate(stdout.String(), 4000, 4000),
		Stderr:   headTailTruncate(stderr.String(), 4000, 4000),
		Command:  command,
	}), nil
}

// Execute satisfies the Tool interface for non-DAG callers; the dispatcher
// prefers ExecuteTyped (no round-trip) and reads the typed body directly.
func (b *Bash) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(b.ExecuteTyped(ctx, params))
}

// bashData is the structured payload of a command node: exit status and the
// captured streams, so consumers read them as typed fields instead of grepping
// the raw string.
type bashData struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Command  string `json:"command"`
}

var _ toolapi.Tool = (*Bash)(nil)

// headTailTruncate keeps the first `head` bytes and last `tail` bytes of s,
// inserting a "... [N bytes elided] ..." separator if truncation happened.
// Critical for Python tracebacks and most CLI errors, where the actionable
// line is at the END (e.g. "OverflowError: date value out of range") and a
// head-only truncation would discard it. Short inputs pass through.
func headTailTruncate(s string, head, tail int) string {
	if len(s) <= head+tail {
		return s
	}
	elided := len(s) - head - tail
	return s[:head] + fmt.Sprintf("\n... [%d bytes elided] ...\n", elided) + s[len(s)-tail:]
}

// writerFunc lets a plain function stand in as an io.Writer, so the command's
// output can be watched without copying it a second time.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// runUntilIdle runs cmd and kills it when it has produced no output for idle.
//
// The whole process group goes, not just the shell: a command that backgrounds
// something leaves the child running otherwise, and the group is why
// SysProcAttr.Setpgid is set above.
//
// An idle of zero means never kill for silence — the caller has said the
// command may be quiet for as long as it likes, and the run's wall clock is
// what stops it.
func runUntilIdle(ctx context.Context, cmd *exec.Cmd, idle time.Duration, lastOutput *atomic.Int64) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if idle <= 0 {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			killGroup(cmd)
			<-done
			return ctx.Err()
		}
	}

	// A tick well under the limit, so the kill lands close to the limit rather
	// than up to a whole limit late.
	tick := idle / 10
	if tick < 100*time.Millisecond {
		tick = 100 * time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()

	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			killGroup(cmd)
			<-done
			return ctx.Err()
		case <-t.C:
			since := time.Since(time.Unix(0, lastOutput.Load()))
			if since >= idle {
				killGroup(cmd)
				<-done
				return fmt.Errorf("no output for %s", idle)
			}
		}
	}
}

// killGroup kills the command's whole process group, not only the shell.
//
// A command that backgrounds something — `npx vite &`, a build that spawns
// workers — leaves those children running when only the shell is signalled, and
// they hold the port or the file the next step needs. Setpgid above is what
// makes the group addressable; a negative pid is the group.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		// No group (or not a platform with them): the process itself still goes.
		_ = cmd.Process.Kill()
	}
}
