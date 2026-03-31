package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	agenttools "github.com/user/kaiju/internal/agent/tools"
)

// ─── EnvList ────────────────────────────────────────────────────────────────

/*
 * EnvList lists or searches environment variables with automatic masking of sensitive values.
 * desc: Tool that returns sorted environment variables, masking those with keys containing PASSWORD, SECRET, TOKEN, etc.
 */
type EnvList struct{}

/*
 * NewEnvList creates a new EnvList tool instance.
 * desc: Returns a zero-value EnvList ready for use.
 * return: pointer to a new EnvList
 */
func NewEnvList() *EnvList { return &EnvList{} }

/*
 * Name returns the tool identifier.
 * desc: Returns "env_list" as the tool name.
 * return: the string "env_list"
 */
func (e *EnvList) Name() string { return "env_list" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains that this tool lists environment variables with automatic sensitive value masking.
 * return: description string
 */
func (e *EnvList) Description() string {
	return "List or search environment variables. Sensitive values (passwords, tokens, keys) are automatically masked."
}

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always returns ImpactObserve since listing environment variables is non-destructive.
 * param: _ - unused parameters
 * return: ImpactObserve (0)
 */
func (e *EnvList) Impact(map[string]any) int { return agenttools.ImpactObserve }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing environment variables as newline-separated text.
 * return: JSON schema as raw bytes
 */
func (e *EnvList) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"output":{"type":"string","description":"environment variables, one per line"}}}`)
}

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines the optional filter (case-insensitive substring) and show_sensitive boolean parameters.
 * return: JSON schema as raw bytes
 */
func (e *EnvList) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filter": {"type": "string", "description": "Filter by variable name (substring match, case-insensitive)"},
			"show_sensitive": {"type": "boolean", "description": "Show full values of sensitive variables (default: false)"}
		},
		"additionalProperties": false
	}`)
}

/*
 * Execute lists environment variables, optionally filtered and with sensitive values masked.
 * desc: Returns sorted KEY=VALUE pairs, masking sensitive keys unless show_sensitive is true.
 * param: _ - unused context
 * param: params - optionally contains "filter" and "show_sensitive"
 * return: newline-separated environment variables, or "no matching environment variables" if none match
 */
func (e *EnvList) Execute(_ context.Context, params map[string]any) (string, error) {
	filter, _ := params["filter"].(string)
	showSensitive, _ := params["show_sensitive"].(bool)

	envs := os.Environ()
	sort.Strings(envs)

	var result []string
	for _, env := range envs {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, value := parts[0], parts[1]

		if filter != "" && !strings.Contains(strings.ToLower(key), strings.ToLower(filter)) {
			continue
		}

		if !showSensitive && isSensitiveKey(key) {
			value = "****"
		}

		result = append(result, fmt.Sprintf("%s=%s", key, value))
	}

	if len(result) == 0 {
		return "no matching environment variables", nil
	}
	return strings.Join(result, "\n"), nil
}

/*
 * isSensitiveKey checks whether an environment variable key contains sensitive keywords.
 * desc: Returns true if the key contains PASSWORD, SECRET, TOKEN, KEY, CREDENTIAL, AUTH, PRIVATE, API_KEY, or APIKEY.
 * param: key - environment variable name to check
 * return: true if the key matches a sensitive keyword pattern
 */
func isSensitiveKey(key string) bool {
	upper := strings.ToUpper(key)
	sensitive := []string{"PASSWORD", "SECRET", "TOKEN", "KEY", "CREDENTIAL", "AUTH", "PRIVATE", "API_KEY", "APIKEY"}
	for _, s := range sensitive {
		if strings.Contains(upper, s) {
			return true
		}
	}
	return false
}

var _ agenttools.Tool = (*EnvList)(nil)

// ─── DiskUsage ──────────────────────────────────────────────────────────────

/*
 * DiskUsage shows disk space usage for mounted filesystems or a specific directory.
 * desc: Tool that runs df/du (Unix) or Get-PSDrive/Get-ChildItem (Windows) to report disk usage.
 */
type DiskUsage struct{}

/*
 * NewDiskUsage creates a new DiskUsage tool instance.
 * desc: Returns a zero-value DiskUsage ready for use.
 * return: pointer to a new DiskUsage
 */
func NewDiskUsage() *DiskUsage { return &DiskUsage{} }

/*
 * Name returns the tool identifier.
 * desc: Returns "disk_usage" as the tool name.
 * return: the string "disk_usage"
 */
func (d *DiskUsage) Name() string { return "disk_usage" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains that this tool shows disk space for all filesystems or a specific path.
 * return: description string
 */
func (d *DiskUsage) Description() string {
	return "Show disk space usage for mounted filesystems, or directory size for a specific path."
}

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always returns ImpactObserve since checking disk usage is non-destructive.
 * param: _ - unused parameters
 * return: ImpactObserve (0)
 */
func (d *DiskUsage) Impact(map[string]any) int { return agenttools.ImpactObserve }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing the disk usage report as a string.
 * return: JSON schema as raw bytes
 */
func (d *DiskUsage) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"output":{"type":"string","description":"disk usage report"}}}`)
}

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines the optional path parameter (omit for all filesystems).
 * return: JSON schema as raw bytes
 */
func (d *DiskUsage) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Specific path to check size of (omit for all filesystems)"}
		},
		"additionalProperties": false
	}`)
}

/*
 * Execute reports disk usage for all filesystems or a specific path.
 * desc: Routes to diskUsageAll for root/empty path, or diskUsagePath for a specific directory.
 * param: ctx - context for cancellation and timeout
 * param: params - optionally contains "path"
 * return: disk usage report string, or error on command failure
 */
func (d *DiskUsage) Execute(ctx context.Context, params map[string]any) (string, error) {
	path, _ := params["path"].(string)

	if path == "" || path == "/" {
		// Root or no path: use df -h (instant) + du top-level breakdown
		return diskUsageAll(ctx)
	}
	return diskUsagePath(ctx, path)
}

/*
 * diskUsageAll returns disk usage for all mounted filesystems.
 * desc: Runs df -h for filesystem overview, plus du for top-level directories over 100MB on Unix.
 * param: ctx - context for cancellation and timeout
 * return: formatted disk usage report, or error on command failure
 */
func diskUsageAll(ctx context.Context) (string, error) {
	var result strings.Builder

	// Part 1: filesystem overview (instant)
	var dfCmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		dfCmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command",
			"Get-PSDrive -PSProvider FileSystem | Select-Object Name, @{N='UsedGB';E={[math]::Round($_.Used/1GB,2)}}, @{N='FreeGB';E={[math]::Round($_.Free/1GB,2)}} | Format-Table -AutoSize")
	default:
		dfCmd = exec.CommandContext(ctx, "df", "-h")
	}
	if out, err := dfCmd.CombinedOutput(); err == nil {
		result.WriteString(strings.TrimSpace(string(out)))
	}

	// Part 2: top-level directory sizes (with timeout)
	if runtime.GOOS != "windows" {
		timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		duCmd := exec.CommandContext(timeoutCtx, "du", "-h", "--max-depth=1", "--threshold=100M", "/")
		if out, err := duCmd.CombinedOutput(); err == nil {
			result.WriteString("\n\nTop-level directories (>100MB):\n")
			result.WriteString(strings.TrimSpace(string(out)))
		}
	}

	return result.String(), nil
}

/*
 * diskUsagePath returns disk usage for a specific directory path.
 * desc: Runs du with max-depth=1 and a 15-second timeout, returning subdirectory sizes over 1MB.
 * param: ctx - context for cancellation
 * param: path - directory path to analyze
 * return: formatted disk usage for the path (truncated to 4KB), or error on failure/timeout
 */
func diskUsagePath(ctx context.Context, path string) (string, error) {
	// Use --max-depth=1 to avoid traversing entire trees, with a 15s timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(timeoutCtx, "powershell", "-NoProfile", "-Command",
			fmt.Sprintf("Get-ChildItem -Force '%s' | ForEach-Object { $size = 0; if(!$_.PSIsContainer){ $size = $_.Length } else { $size = (Get-ChildItem -Recurse -Force $_.FullName -ErrorAction SilentlyContinue | Measure-Object -Property Length -Sum).Sum }; [PSCustomObject]@{Name=$_.Name; SizeMB=[math]::Round($size/1MB,1)} } | Sort-Object SizeMB -Descending | Format-Table -AutoSize", path))
	default:
		cmd = exec.CommandContext(timeoutCtx, "du", "-h", "--max-depth=1", "--threshold=1M", path)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		if timeoutCtx.Err() == context.DeadlineExceeded {
			// Return partial output if we got some before timeout
			if len(out) > 0 {
				return strings.TrimSpace(string(out)) + "\n(truncated — scan timed out)", nil
			}
			return "", fmt.Errorf("disk_usage: scan timed out after 15s")
		}
		return "", fmt.Errorf("disk_usage: %w", err)
	}

	output := strings.TrimSpace(string(out))
	if len(output) > 4096 {
		output = output[:4096] + "\n... (truncated)"
	}
	return output, nil
}

var _ agenttools.Tool = (*DiskUsage)(nil)

// ─── Clipboard ──────────────────────────────────────────────────────────────

/*
 * Clipboard reads or writes the system clipboard.
 * desc: Tool that accesses the system clipboard via pbcopy/pbpaste (macOS), xclip/xsel (Linux), or PowerShell (Windows).
 */
type Clipboard struct{}

/*
 * NewClipboard creates a new Clipboard tool instance.
 * desc: Returns a zero-value Clipboard ready for use.
 * return: pointer to a new Clipboard
 */
func NewClipboard() *Clipboard { return &Clipboard{} }

/*
 * Name returns the tool identifier.
 * desc: Returns "clipboard" as the tool name.
 * return: the string "clipboard"
 */
func (c *Clipboard) Name() string { return "clipboard" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains that this tool reads from or writes to the system clipboard.
 * return: description string
 */
func (c *Clipboard) Description() string { return "Read from or write to the system clipboard." }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing clipboard content or a confirmation message.
 * return: JSON schema as raw bytes
 */
func (c *Clipboard) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"content":{"type":"string","description":"clipboard content or confirmation"}}}`)
}

/*
 * Impact determines the safety level based on the clipboard action.
 * desc: Returns ImpactAffect for write actions, ImpactObserve for read actions.
 * param: params - must contain "action" to determine impact level
 * return: ImpactAffect (1) for write, ImpactObserve (0) for read
 */
func (c *Clipboard) Impact(params map[string]any) int {
	action, _ := params["action"].(string)
	if action == "write" {
		return agenttools.ImpactAffect
	}
	return agenttools.ImpactObserve
}

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines the required action (read/write) and optional content for write operations.
 * return: JSON schema as raw bytes
 */
func (c *Clipboard) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["read", "write"], "description": "Read from or write to clipboard"},
			"content": {"type": "string", "description": "Content to write (required for write action)"}
		},
		"required": ["action"],
		"additionalProperties": false
	}`)
}

/*
 * Execute performs the specified clipboard action (read or write).
 * desc: Routes to clipboardRead or clipboardWrite based on the action parameter.
 * param: ctx - context for cancellation
 * param: params - must contain "action"; "content" required for write
 * return: clipboard content (for read) or confirmation message (for write), or error on failure
 */
func (c *Clipboard) Execute(ctx context.Context, params map[string]any) (string, error) {
	action, _ := params["action"].(string)

	switch action {
	case "read":
		return clipboardRead(ctx)
	case "write":
		content, _ := params["content"].(string)
		return clipboardWrite(ctx, content)
	default:
		return "", fmt.Errorf("clipboard: action must be 'read' or 'write'")
	}
}

/*
 * clipboardRead reads content from the system clipboard.
 * desc: Uses pbpaste (macOS), Get-Clipboard (Windows), or xclip/xsel (Linux) to read clipboard content.
 * param: ctx - context for cancellation
 * return: clipboard content string (truncated to 8KB), or error if clipboard tools are unavailable
 */
func clipboardRead(ctx context.Context) (string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "pbpaste")
	case "windows":
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", "Get-Clipboard")
	default:
		// Try xclip first, fall back to xsel
		cmd = exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-o")
	}

	out, err := cmd.Output()
	if err != nil {
		// Linux fallback
		if runtime.GOOS == "linux" {
			cmd = exec.CommandContext(ctx, "xsel", "--clipboard", "--output")
			out, err = cmd.Output()
			if err != nil {
				return "", fmt.Errorf("clipboard: install xclip or xsel for clipboard access")
			}
		} else {
			return "", fmt.Errorf("clipboard: %w", err)
		}
	}

	content := string(out)
	if len(content) > 8192 {
		content = content[:8192] + "\n... (truncated)"
	}
	return content, nil
}

/*
 * clipboardWrite writes content to the system clipboard.
 * desc: Uses pbcopy (macOS), Set-Clipboard (Windows), or xclip/xsel (Linux) to write to clipboard.
 * param: ctx - context for cancellation
 * param: content - text content to write to the clipboard
 * return: confirmation message with byte count, or error if content is empty or clipboard tools are unavailable
 */
func clipboardWrite(ctx context.Context, content string) (string, error) {
	if content == "" {
		return "", fmt.Errorf("clipboard: content is required for write")
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "pbcopy")
	case "windows":
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", "Set-Clipboard -Value $input")
	default:
		cmd = exec.CommandContext(ctx, "xclip", "-selection", "clipboard")
	}

	cmd.Stdin = strings.NewReader(content)
	if err := cmd.Run(); err != nil {
		if runtime.GOOS == "linux" {
			cmd = exec.CommandContext(ctx, "xsel", "--clipboard", "--input")
			cmd.Stdin = strings.NewReader(content)
			if err := cmd.Run(); err != nil {
				return "", fmt.Errorf("clipboard: install xclip or xsel for clipboard access")
			}
		} else {
			return "", fmt.Errorf("clipboard: %w", err)
		}
	}

	return fmt.Sprintf("wrote %d bytes to clipboard", len(content)), nil
}

var _ agenttools.Tool = (*Clipboard)(nil)
