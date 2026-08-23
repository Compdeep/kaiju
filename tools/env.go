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

	"github.com/Compdeep/kaiju/agent/toolapi"
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
func (e *EnvList) Impact(map[string]any) int { return toolapi.ImpactObserve }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing environment variables as newline-separated text.
 * return: JSON schema as raw bytes
 */
func (e *EnvList) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(envListData{}))
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
// Execute satisfies the Tool interface for callers outside the DAG.
func (e *EnvList) Execute(_ context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(e.ExecuteTyped(nil, params))
}

func (e *EnvList) ExecuteTyped(_ context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	filter, _ := params["filter"].(string)
	showSensitive, _ := params["show_sensitive"].(bool)

	envs := os.Environ()
	sort.Strings(envs)

	var result []string
	data := envListData{Filter: filter, Variables: map[string]string{}}
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
			data.Masked++
		}

		result = append(result, fmt.Sprintf("%s=%s", key, value))
		data.Variables[key] = value
	}

	if len(result) == 0 {
		return toolapi.ToolEmpty("env", "no matching environment variables"), nil
	}
	// The same pairs as fields, so a later step can name one variable instead of
	// being handed the whole listing to read. Masking happens above, so a value
	// here is masked exactly as it is in the text.
	data.Count = len(result)
	return toolapi.ToolOK("env", strings.Join(result, "\n"), data), nil
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

var _ toolapi.Tool = (*EnvList)(nil)

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
func (d *DiskUsage) Impact(map[string]any) int { return toolapi.ImpactObserve }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing the disk usage report as a string.
 * return: JSON schema as raw bytes
 */
func (d *DiskUsage) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(diskUsageData{}))
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
// Execute satisfies the Tool interface for callers outside the DAG.
func (d *DiskUsage) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(d.ExecuteTyped(ctx, params))
}

func (d *DiskUsage) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
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
func diskUsageAll(ctx context.Context) (toolapi.ToolMessage, error) {
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
	data := diskUsageData{Complete: true}
	if out, err := dfCmd.CombinedOutput(); err == nil {
		result.WriteString(strings.TrimSpace(string(out)))
		data.Filesystems = parseDfTable(string(out))
	} else {
		// The overview is the first half of this answer. Without it the report is
		// the directory sizes alone, which is not what was asked for.
		data.Complete = false
	}

	// Part 2: top-level directory sizes (with timeout)
	if runtime.GOOS != "windows" {
		timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		duCmd := exec.CommandContext(timeoutCtx, "du", "-h", "--max-depth=1", "--threshold=100M", "/")
		out, err := duCmd.CombinedOutput()
		if len(out) > 0 {
			result.WriteString("\n\nTop-level directories (>100MB):\n")
			result.WriteString(strings.TrimSpace(string(out)))
		}
		// du walking / from this process reaches directories it may not read, so
		// a non-zero exit here is expected rather than exceptional. What it did
		// read is kept either way, and the directories it was refused are named,
		// because their contents are in none of the sizes above.
		entries, unreadable := parseDuLines(string(out))
		data.Entries = append(data.Entries, entries...)
		data.Unreadable = append(data.Unreadable, unreadable...)
		if err != nil || len(unreadable) > 0 {
			data.Complete = false
		}
	}

	return toolapi.ToolOK("disk", result.String(), data), nil
}

/*
 * diskUsagePath returns disk usage for a specific directory path.
 * desc: Runs du with max-depth=1 and a 15-second timeout, returning subdirectory sizes over 1MB.
 * param: ctx - context for cancellation
 * param: path - directory path to analyze
 * return: formatted disk usage for the path (truncated to 4KB), or error on failure/timeout
 */
func diskUsagePath(ctx context.Context, path string) (toolapi.ToolMessage, error) {
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
				entries, unreadable := parseDuLines(string(out))
				return toolapi.ToolOK("disk",
					strings.TrimSpace(string(out))+"\n(truncated — scan timed out)",
					diskUsageData{Path: path, Entries: entries, Unreadable: unreadable, Complete: false}), nil
			}
			return toolapi.ToolMessage{}, fmt.Errorf("disk_usage: scan timed out after 15s")
		}

		// du exits non-zero when it cannot read a directory, having already
		// printed the sizes it could read and a line per directory it could not.
		// Discarding that left "exit status 1" as the whole answer, which is what
		// asking for a path with anyone else's files under it produced — /tmp on
		// a shared machine, every time.
		//
		// So the output it captured survives, and the status says the answer is
		// incomplete rather than absent. Same shape as network_diag: a command
		// that failed still said something, and what it said is the useful part.
		if text := strings.TrimSpace(string(out)); text != "" {
			entries, unreadable := parseDuLines(string(out))
			msg := toolapi.ToolFail("disk",
				"du could not read every directory under "+path+
					", so this listing is incomplete: "+err.Error(),
				diskUsageData{Path: path, Entries: entries, Unreadable: unreadable, Complete: false})
			// ToolFail carries the reason and the fields; the listing du printed
			// before it was refused goes in content, which is the whole point of
			// this branch.
			msg.Content = text
			return msg, nil
		}
		return toolapi.ToolMessage{}, fmt.Errorf("disk_usage: %w", err)
	}

	// Parsed before the cut, so the fields describe the directory rather than the
	// first 4KB of the listing of it.
	entries, unreadable := parseDuLines(string(out))

	output := strings.TrimSpace(string(out))
	truncated := false
	if len(output) > 4096 {
		output = output[:4096] + "\n... (truncated)"
		truncated = true
	}
	return toolapi.ToolOK("disk", output, diskUsageData{
		Path: path, Entries: entries, Unreadable: unreadable,
		Complete: len(unreadable) == 0, Truncated: truncated,
	}), nil
}

var _ toolapi.Tool = (*DiskUsage)(nil)

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
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(clipboardData{}))
}

/*
 * Impact determines the safety level based on the clipboard action.
 * desc: Returns ImpactAffect for write actions, ImpactObserve for read actions.
 * param: params - must contain "action" to determine impact level
 * return: ImpactAffect (1) for write, ImpactObserve (0) for read
 */
func (c *Clipboard) Impact(params map[string]any) int {
	action, _ := params["action"].(string)
	// No action is the abstract question, answered with the worst this tool
	// can do — it can write the clipboard.
	if action == "" {
		return toolapi.ImpactAffect
	}
	if action == "write" {
		return toolapi.ImpactAffect
	}
	return toolapi.ImpactObserve
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
		"allOf": [
			{"if": {"properties": {"action": {"const": "write"}}, "required": ["action"]},
			 "then": {"required": ["content"]}}
		],
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
// Execute satisfies the Tool interface for callers outside the DAG.
func (c *Clipboard) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(c.ExecuteTyped(ctx, params))
}

func (c *Clipboard) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	action, _ := params["action"].(string)

	switch action {
	case "read":
		return clipboardRead(ctx)
	case "write":
		content, _ := params["content"].(string)
		return clipboardWrite(ctx, content)
	default:
		return toolapi.ToolMessage{}, fmt.Errorf("clipboard: action must be 'read' or 'write'")
	}
}

/*
 * clipboardRead reads content from the system clipboard.
 * desc: Uses pbpaste (macOS), Get-Clipboard (Windows), or xclip/xsel (Linux) to read clipboard content.
 * param: ctx - context for cancellation
 * return: clipboard content string (truncated to 8KB), or error if clipboard tools are unavailable
 */
func clipboardRead(ctx context.Context) (toolapi.ToolMessage, error) {
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
				// No clipboard program on this machine. A failure to look, said as
				// one: a Go error ends the step, so a run that asked about the
				// clipboard as one part of a wider question lost the rest of it —
				// over a machine that simply has no graphical session.
				return toolapi.ToolFail("clipboard",
					"this machine has no clipboard to read: neither xclip nor xsel is "+
						"installed, which is usual on a server with no graphical session",
					clipboardData{Action: "read"}), nil
			}
		} else {
			return toolapi.ToolMessage{}, fmt.Errorf("clipboard: %w", err)
		}
	}

	content := string(out)
	// Measured before the cut, so a step reading bytes learns the size of what is
	// on the clipboard and not the size of what fitted.
	data := clipboardData{Action: "read", Bytes: len(content)}
	if len(content) > 8192 {
		content = content[:8192] + "\n... (truncated)"
		data.Truncated = true
	}
	return toolapi.ToolOK("clipboard", content, data), nil
}

/*
 * clipboardWrite writes content to the system clipboard.
 * desc: Uses pbcopy (macOS), Set-Clipboard (Windows), or xclip/xsel (Linux) to write to clipboard.
 * param: ctx - context for cancellation
 * param: content - text content to write to the clipboard
 * return: confirmation message with byte count, or error if content is empty or clipboard tools are unavailable
 */
func clipboardWrite(ctx context.Context, content string) (toolapi.ToolMessage, error) {
	if content == "" {
		return toolapi.ToolMessage{}, fmt.Errorf("clipboard: content is required for write")
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
				// As on the read side: a machine with no clipboard program is a
				// fact about the machine, and a Go error would end a run that asked
				// about the clipboard as one part of a wider question.
				return toolapi.ToolFail("clipboard",
					"this machine has no clipboard to write to: neither xclip nor xsel is "+
						"installed, which is usual on a server with no graphical session",
					clipboardData{Action: "write", Bytes: len(content)}), nil
			}
		} else {
			return toolapi.ToolMessage{}, fmt.Errorf("clipboard: %w", err)
		}
	}

	return toolapi.ToolOK("clipboard", fmt.Sprintf("wrote %d bytes to clipboard", len(content)),
		clipboardData{Action: "write", Bytes: len(content)}), nil
}

var _ toolapi.Tool = (*Clipboard)(nil)
