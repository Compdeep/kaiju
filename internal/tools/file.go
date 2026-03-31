package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/user/kaiju/internal/agent/tools"
)

// ─── FileRead ───────────────────────────────────────────────────────────────

/*
 * FileRead reads a file's contents with optional line limit.
 * desc: Tool that reads file content as text, truncating at a configurable max line count.
 */
type FileRead struct{}

/*
 * NewFileRead creates a new FileRead tool instance.
 * desc: Returns a zero-value FileRead ready for use.
 * return: pointer to a new FileRead
 */
func NewFileRead() *FileRead { return &FileRead{} }

/*
 * Name returns the tool identifier.
 * desc: Returns "file_read" as the tool name.
 * return: the string "file_read"
 */
func (f *FileRead) Name() string { return "file_read" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains that this tool reads file contents as text.
 * return: description string
 */
func (f *FileRead) Description() string {
	return "Read the contents of a file. Returns the file content as text."
}

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always returns ImpactObserve since reading files is non-destructive.
 * param: _ - unused parameters
 * return: ImpactObserve (0)
 */
func (f *FileRead) Impact(map[string]any) int { return tools.ImpactObserve }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing file contents as a string.
 * return: JSON schema as raw bytes
 */
func (f *FileRead) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"content":{"type":"string","description":"file contents"}}}`)
}

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines the required path and optional max_lines parameters.
 * return: JSON schema as raw bytes
 */
func (f *FileRead) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Path to the file to read"},
			"max_lines": {"type": "integer", "description": "Maximum lines to read (default: 500)"}
		},
		"required": ["path"],
		"additionalProperties": false
	}`)
}

/*
 * Execute reads the file at the given path and returns its content.
 * desc: Reads the file, splits into lines, and truncates at max_lines (default 500).
 * param: _ - unused context
 * param: params - must contain "path"; optionally "max_lines"
 * return: file content as a string (possibly truncated), or error if file cannot be read
 */
func (f *FileRead) Execute(_ context.Context, params map[string]any) (string, error) {
	path, _ := params["path"].(string)
	if path == "" {
		return "", fmt.Errorf("file_read: path is required")
	}
	path = filepath.Clean(path)

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("file_read: %w", err)
	}

	maxLines := 500
	if ml, ok := params["max_lines"].(float64); ok && ml > 0 {
		maxLines = int(ml)
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines = append(lines, fmt.Sprintf("... (truncated at %d lines)", maxLines))
	}

	return strings.Join(lines, "\n"), nil
}

var _ tools.Tool = (*FileRead)(nil)

// ─── FileWrite ──────────────────────────────────────────────────────────────

/*
 * FileWrite writes content to a file, creating or overwriting it.
 * desc: Tool that writes string content to a file path, with optional append mode.
 */
type FileWrite struct{}

/*
 * NewFileWrite creates a new FileWrite tool instance.
 * desc: Returns a zero-value FileWrite ready for use.
 * return: pointer to a new FileWrite
 */
func NewFileWrite() *FileWrite { return &FileWrite{} }

/*
 * Name returns the tool identifier.
 * desc: Returns "file_write" as the tool name.
 * return: the string "file_write"
 */
func (f *FileWrite) Name() string { return "file_write" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains that this tool writes content to a file.
 * return: description string
 */
func (f *FileWrite) Description() string {
	return "Write content to a file. Creates the file if it doesn't exist, or overwrites it."
}

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always returns ImpactAffect since writing files modifies the filesystem.
 * param: _ - unused parameters
 * return: ImpactAffect (1)
 */
func (f *FileWrite) Impact(map[string]any) int { return tools.ImpactAffect }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing a confirmation message with bytes written.
 * return: JSON schema as raw bytes
 */
func (f *FileWrite) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"result":{"type":"string","description":"confirmation message with bytes written"}}}`)
}

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines path, content, and optional append parameters.
 * return: JSON schema as raw bytes
 */
func (f *FileWrite) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Path to write to"},
			"content": {"type": "string", "description": "Content to write"},
			"append": {"type": "boolean", "description": "Append instead of overwrite (default: false)"}
		},
		"required": ["path", "content"],
		"additionalProperties": false
	}`)
}

/*
 * Execute writes content to the specified file path.
 * desc: Creates parent directories if needed, then writes or appends content to the file.
 * param: _ - unused context
 * param: params - must contain "path" and "content"; optionally "append" for append mode
 * return: confirmation message with byte count written, or error on failure
 */
func (f *FileWrite) Execute(_ context.Context, params map[string]any) (string, error) {
	path, _ := params["path"].(string)
	content, _ := params["content"].(string)
	if path == "" {
		return "", fmt.Errorf("file_write: path is required")
	}
	path = filepath.Clean(path)

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("file_write: create dir: %w", err)
	}

	appendMode, _ := params["append"].(bool)
	if appendMode {
		f2, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return "", fmt.Errorf("file_write: %w", err)
		}
		defer f2.Close()
		if _, err := f2.WriteString(content); err != nil {
			return "", fmt.Errorf("file_write: %w", err)
		}
		return fmt.Sprintf("appended %d bytes to %s", len(content), path), nil
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("file_write: %w", err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
}

/*
 * DisplayHint auto-detects file type and suggests a panel plugin for rendering.
 * desc: Returns a display hint based on the written file's extension for frontend panel rendering.
 * param: params - tool parameters containing the "path" of the written file
 * param: result - the execution result string (unused)
 * return: DisplayHint with plugin/path/title/mime, or nil if no suitable plugin found
 */
func (f *FileWrite) DisplayHint(params map[string]any, result string) *tools.DisplayHint {
	path, _ := params["path"].(string)
	if path == "" {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(path))
	base := filepath.Base(path)

	switch ext {
	case ".html", ".htm":
		return &tools.DisplayHint{Plugin: "preview", Path: path, Title: base, Mime: "text/html"}
	case ".svg":
		return &tools.DisplayHint{Plugin: "preview", Path: path, Title: base, Mime: "image/svg+xml"}
	case ".go", ".js", ".ts", ".py", ".rs", ".java", ".c", ".cpp", ".rb", ".sh",
		".css", ".json", ".yaml", ".yml", ".toml", ".sql", ".md", ".vue", ".jsx", ".tsx":
		return &tools.DisplayHint{Plugin: "code", Path: path, Title: base}
	default:
		return nil
	}
}

var _ tools.Tool = (*FileWrite)(nil)
var _ tools.Displayer = (*FileWrite)(nil)

// ─── FileList ───────────────────────────────────────────────────────────────

/*
 * FileList lists files and directories at a given path.
 * desc: Tool that reads a directory and returns entries with name, type, and size.
 */
type FileList struct{}

/*
 * NewFileList creates a new FileList tool instance.
 * desc: Returns a zero-value FileList ready for use.
 * return: pointer to a new FileList
 */
func NewFileList() *FileList { return &FileList{} }

/*
 * Name returns the tool identifier.
 * desc: Returns "file_list" as the tool name.
 * return: the string "file_list"
 */
func (f *FileList) Name() string { return "file_list" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains that this tool lists files and directories at a path.
 * return: description string
 */
func (f *FileList) Description() string { return "List files and directories at the given path." }

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always returns ImpactObserve since listing files is non-destructive.
 * param: _ - unused parameters
 * return: ImpactObserve (0)
 */
func (f *FileList) Impact(map[string]any) int { return tools.ImpactObserve }

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines the optional path parameter (defaults to current directory).
 * return: JSON schema as raw bytes
 */
func (f *FileList) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Directory path to list (default: current directory)"}
		},
		"additionalProperties": false
	}`)
}

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure as an array of entry objects with name, type, and size.
 * return: JSON schema as raw bytes
 */
func (f *FileList) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"entries":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"type":{"type":"string"},"size":{"type":"integer"}}}}}}`)
}

/*
 * Execute lists files and directories at the given path.
 * desc: Reads the directory and returns a JSON array of entries with name, type (file/dir), and size.
 * param: _ - unused context
 * param: params - optionally contains "path" (defaults to ".")
 * return: JSON string with directory entries, or error if the directory cannot be read
 */
func (f *FileList) Execute(_ context.Context, params map[string]any) (string, error) {
	path, _ := params["path"].(string)
	if path == "" {
		path = "."
	}
	path = filepath.Clean(path)

	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("file_list: %w", err)
	}

	type entry struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Size int64  `json:"size"`
	}

	result := make([]entry, 0, len(entries))
	for _, e := range entries {
		typ := "file"
		if e.IsDir() {
			typ = "dir"
		}
		var size int64
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		result = append(result, entry{Name: e.Name(), Type: typ, Size: size})
	}

	b, _ := json.Marshal(map[string]any{"entries": result})
	return string(b), nil
}

var _ tools.Tool = (*FileList)(nil)
var _ tools.Outputter = (*FileList)(nil)
