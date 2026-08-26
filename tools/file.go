package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/agent/toolapi"
	"github.com/Compdeep/kaiju/internal/workspace"
)

// ─── FileRead ───────────────────────────────────────────────────────────────

/*
 * FileRead reads a file's contents with optional line limit.
 * desc: Tool that reads file content as text, truncating at a configurable max line count.
 */
type FileRead struct {
	workspace string
}

/*
 * NewFileRead creates a new FileRead tool instance.
 * desc: Returns a zero-value FileRead ready for use.
 * return: pointer to a new FileRead
 */
func NewFileRead(workspace string) *FileRead { return &FileRead{workspace: workspace} }

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
func (f *FileRead) Impact(map[string]any) int { return toolapi.ImpactObserve }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing file contents as a string.
 * return: JSON schema as raw bytes
 */
// Excerpts declares that the text of a read is capped at a line count while
// path names the file it came from. A step wired to the text when the file was
// longer than the cap is working from part of it, and the file is still there
// to be read in full by a step that runs over it rather than reads it.
func (f *FileRead) Excerpts() []toolapi.Excerpt {
	return []toolapi.Excerpt{{
		Field: "content",
		Whole: "path",
		Flag:  "truncated",
		Use:   "read path in this step: the text above stops at this tool's line cap, and that file is all of it",
	}}
}

func (f *FileRead) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(fileReadData{}))
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
			"max_lines": {"type": "integer", "description": "Read the FIRST N lines (default: 500)"},
			"tail_lines": {"type": "integer", "description": "Read the LAST N lines instead — for a log, where the interesting part is at the bottom"}
		},
		"required": ["path"],
		"additionalProperties": false
	}`)
}

/*
 * Execute reads the file at the given path and returns its content.
 * desc: Reads the file, splits into lines, and truncates at max_lines (default 500).
 * param: _ - unused context
 * param: params - must contain "path"; optionally "max_lines" or "tail_lines"
 * return: file content as a string (possibly truncated), or error if file cannot be read
 */
// Execute satisfies the Tool interface for callers outside the DAG.
func (f *FileRead) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(f.ExecuteTyped(ctx, params))
}

func (f *FileRead) ExecuteTyped(_ context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	path, _ := params["path"].(string)
	if path == "" {
		return toolapi.ToolMessage{}, fmt.Errorf("file_read: path is required")
	}
	// Resolve relative paths against workspace
	if !filepath.IsAbs(path) && f.workspace != "" {
		path = filepath.Join(f.workspace, path)
	}
	path = filepath.Clean(path)

	fh, err := os.Open(path)
	if err != nil {
		return toolapi.ToolMessage{}, fmt.Errorf("file_read: %w", err)
	}
	defer fh.Close()

	maxLines := 500
	if ml, ok := toolapi.ParamNum(params, "max_lines"); ok && ml > 0 {
		maxLines = int(ml)
	}
	tailLines := 0
	if tl, ok := toolapi.ParamNum(params, "tail_lines"); ok && tl > 0 {
		tailLines = int(tl)
	}

	// Streamed, not slurped. This read the whole file into memory and then threw
	// away all but a few lines, so asking for the last five lines of a 200MB log
	// allocated 412MB — measured, not estimated. A log is exactly what this tool
	// is pointed at, and an agent reading one on someone's machine should not
	// need the file's size in memory to do it.
	//
	// Head keeps at most maxLines. Tail keeps a ring of tailLines and lets the
	// rest go. Either way the cost is the lines asked for, whatever the file.
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // a single very long line
	var head []string
	var ring []string
	total := 0
	for sc.Scan() {
		total++
		line := sc.Text()
		if tailLines > 0 {
			ring = append(ring, line)
			if len(ring) > tailLines {
				ring = ring[1:]
			}
			continue
		}
		if len(head) < maxLines {
			head = append(head, line)
		}
	}
	if err := sc.Err(); err != nil {
		return toolapi.ToolMessage{}, fmt.Errorf("file_read: %w", err)
	}

	// An empty file is something to report, not nothing to report. Reported as
	// empty so the coverage statement can say the file had nothing in it, rather
	// than the model inferring content from a silent gap. Distinct from a missing
	// file, which fails the open above and fails the node.
	if total == 0 {
		return toolapi.ToolEmpty("text", "the file is empty: "+path), nil
	}

	// The counts were in the text and nowhere else, so a step that needed to know
	// whether it had the whole file had to read a parenthesis out of prose.
	if tailLines > 0 {
		shown := len(ring)
		if total > tailLines {
			ring = append([]string{fmt.Sprintf("... (showing the last %d of %d lines)", tailLines, total)}, ring...)
		}
		return toolapi.ToolOK("text", strings.Join(ring, "\n"), fileReadData{
			Path: path, LinesShown: shown, LinesTotal: total,
			Truncated: total > tailLines, FromEnd: true,
		}), nil
	}
	shown := len(head)
	if total > maxLines {
		head = append(head, fmt.Sprintf("... (truncated at %d of %d lines)", maxLines, total))
	}
	return toolapi.ToolOK("text", strings.Join(head, "\n"), fileReadData{
		Path: path, LinesShown: shown, LinesTotal: total, Truncated: total > maxLines,
	}), nil
}

var _ toolapi.Tool = (*FileRead)(nil)

// ─── FileWrite ──────────────────────────────────────────────────────────────

/*
 * FileWrite writes content to a file, creating or overwriting it.
 * desc: Tool that writes string content to a file path, with optional append mode.
 */
type FileWrite struct {
	where PathPolicy
}

// PathPolicy decides where a write may land, or refuses it.
//
// This exists because the rule was built into the tool. Every write went through
// SafeJoin against a workspace, which rejects absolute paths and anything outside five
// named subdirectories — the right rule for an assistant editing a project, and the
// reason a coder step could not overwrite cmd/kaiju/main.go again after it did on
// 2026-04-18. It is the wrong rule for every other kind of application, and there was no
// way to supply a different one, so an application that writes elsewhere had to write its
// own tool with the same name.
//
// The tool does the writing. What may be written is the application's to decide, exactly
// as ProcessKill kills and leaves the question of whether it should to its caller.
//
// param: path - the path the caller asked for.
// return: the path to write, or an error explaining the refusal to whoever asked.
type PathPolicy func(path string) (string, error)

/*
 * NewFileWrite creates a new FileWrite tool instance.
 * desc: Writes wherever it is told when where is nil, which is what an application
 *       operating on machines outside its own directory needs. Pass a policy to confine
 *       it; ConfineToWorkspace is the one this module's own program uses.
 * param: where - the policy, or nil for no restriction beyond the operating system's.
 * return: pointer to a new FileWrite
 */
func NewFileWrite(where PathPolicy) *FileWrite { return &FileWrite{where: where} }

/*
 * ConfineToWorkspace returns the policy this module's own program uses: writes land
 * inside the workspace's allowed subdirectories and nowhere else.
 *
 * param: dir - the workspace root.
 * return: a policy refusing absolute paths, parent-directory escapes, and anything
 *         outside the allowed subdirectories.
 */
/*
 * WorkspaceDefault returns the policy that treats the workspace as where work
 * lands by default rather than the only place it may land.
 * desc: A relative path resolves under the workspace with the same escape and
 *       zone rules ConfineToWorkspace applies. A path with a root is taken as
 *       given, unless it lands inside the workspace, where the zone rules apply
 *       again — so writing to the agent's own tree cannot be done by spelling
 *       the path out in full. A write outside is graded by the gate, which sees
 *       the intent and clearance a path rule cannot.
 * param: dir - the workspace root.
 * return: a policy that defaults to the workspace and permits a named location.
 */
func WorkspaceDefault(dir string) PathPolicy {
	return func(path string) (string, error) { return workspace.Resolve(dir, path) }
}

func ConfineToWorkspace(dir string) PathPolicy {
	return func(path string) (string, error) { return workspace.SafeJoin(dir, path) }
}

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
func (f *FileWrite) Impact(map[string]any) int { return toolapi.ImpactAffect }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure containing a confirmation message with bytes written.
 * return: JSON schema as raw bytes
 */
func (f *FileWrite) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(toolapi.PayloadSchemaOf(fileWriteData{}))
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
			"content": {"type": "string", "minLength": 0, "description": "Content to write. May be empty: \"\" creates an empty file, and truncates an existing one."},
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
// Execute satisfies the Tool interface for callers outside the DAG.
func (f *FileWrite) Execute(_ context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(f.ExecuteTyped(nil, params))
}

func (f *FileWrite) ExecuteTyped(_ context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	path, _ := params["path"].(string)
	content, _ := params["content"].(string)
	if path == "" {
		return toolapi.ToolMessage{}, fmt.Errorf("file_write: path is required")
	}
	// Reject unresolved placeholder content — substitution failed or wasn't wired
	if strings.HasPrefix(content, "${") || strings.HasPrefix(content, "{{") {
		return toolapi.ToolMessage{}, fmt.Errorf("file_write: content is an unresolved placeholder %q — wire ${step.N.field} from an upstream step or use compute instead", content)
	}
	// Where the write may land is the application's rule, not this tool's.
	if f.where != nil {
		safePath, safeErr := f.where(path)
		if safeErr != nil {
			return toolapi.ToolMessage{}, fmt.Errorf("file_write: %w", safeErr)
		}
		path = safePath
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return toolapi.ToolMessage{}, fmt.Errorf("file_write: create dir: %w", err)
	}

	appendMode, _ := params["append"].(bool)
	if appendMode {
		f2, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return toolapi.ToolMessage{}, fmt.Errorf("file_write: %w", err)
		}
		if _, err := f2.WriteString(content); err != nil {
			f2.Close()
			return toolapi.ToolMessage{}, fmt.Errorf("file_write: %w", err)
		}
		// Closing was deferred, which threw the error away. A close that fails after a
		// write that did not means the bytes may never have reached the disk, and
		// reporting that as an append is a lie about what is on the machine.
		if err := f2.Close(); err != nil {
			return toolapi.ToolMessage{}, fmt.Errorf("file_write: finishing %s: %w", path, err)
		}
		return toolapi.ToolOK("status", fmt.Sprintf("appended %d bytes to %s", len(content), path),
			fileWriteData{Path: path, Bytes: len(content), Appended: true}), nil
	}

	if err := agent.OverwriteFile(path, content); err != nil {
		return toolapi.ToolMessage{}, fmt.Errorf("file_write: %w", err)
	}
	return toolapi.ToolOK("status", fmt.Sprintf("wrote %d bytes to %s", len(content), path),
		fileWriteData{Path: path, Bytes: len(content)}), nil
}

/*
 * DisplayHint auto-detects file type and suggests a panel plugin for rendering.
 * desc: Returns a display hint based on the written file's extension for frontend panel rendering.
 * param: params - tool parameters containing the "path" of the written file
 * param: result - the execution result string (unused)
 * return: DisplayHint with plugin/path/title/mime, or nil if no suitable plugin found
 */
func (f *FileWrite) DisplayHint(params map[string]any, result string) *toolapi.DisplayHint {
	path, _ := params["path"].(string)
	if path == "" {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(path))
	base := filepath.Base(path)

	switch ext {
	case ".html", ".htm":
		return &toolapi.DisplayHint{Plugin: "preview", Path: path, Title: base, Mime: "text/html"}
	case ".svg":
		return &toolapi.DisplayHint{Plugin: "preview", Path: path, Title: base, Mime: "image/svg+xml"}
	case ".go", ".js", ".ts", ".py", ".rs", ".java", ".c", ".cpp", ".rb", ".sh",
		".css", ".json", ".yaml", ".yml", ".toml", ".sql", ".md", ".vue", ".jsx", ".tsx":
		return &toolapi.DisplayHint{Plugin: "code", Path: path, Title: base}
	default:
		return nil
	}
}

var _ toolapi.Tool = (*FileWrite)(nil)
var _ toolapi.Displayer = (*FileWrite)(nil)

// ─── FileList ───────────────────────────────────────────────────────────────

/*
 * FileList lists files and directories at a given path.
 * desc: Tool that reads a directory and returns entries with name, type, and size.
 */
type FileList struct {
	workspace string
}

/*
 * NewFileList creates a new FileList tool instance.
 * desc: Returns a zero-value FileList ready for use.
 * return: pointer to a new FileList
 */
func NewFileList(workspace string) *FileList { return &FileList{workspace: workspace} }

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
func (f *FileList) Impact(map[string]any) int { return toolapi.ImpactObserve }

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines the optional path parameter (defaults to current directory).
 * return: JSON schema as raw bytes
 */
func (f *FileList) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Directory path to list (default: workspace)"}
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
	return toolapi.EnvelopeSchema(`{"type":"object","properties":{"entries":{"type":"array","items":{"type":"object","properties":{"name":{"type":"string"},"type":{"type":"string"},"size":{"type":"integer"}}}}}}`)
}

/*
 * Execute lists files and directories at the given path.
 * desc: Reads the directory and returns a JSON array of entries with name, type (file/dir), and size.
 * param: _ - unused context
 * param: params - optionally contains "path" (defaults to ".")
 * return: JSON string with directory entries, or error if the directory cannot be read
 */
// Execute satisfies the Tool interface for callers outside the DAG.
func (f *FileList) Execute(_ context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(f.ExecuteTyped(nil, params))
}

func (f *FileList) ExecuteTyped(_ context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	path, _ := params["path"].(string)
	path = strings.TrimSpace(path)
	if (path == "" || path == "." || path == "./") && f.workspace != "" {
		path = f.workspace
	} else if path == "" {
		path = "."
	} else if !filepath.IsAbs(path) && f.workspace != "" {
		// Resolve relative paths against workspace
		path = filepath.Join(f.workspace, path)
	}
	path = filepath.Clean(path)

	entries, err := os.ReadDir(path)
	if err != nil {
		return toolapi.ToolMessage{}, fmt.Errorf("file_list: %w", err)
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

	return toolapi.ToolOK("listing", "", map[string]any{"entries": result}), nil
}

var _ toolapi.Tool = (*FileList)(nil)
var _ toolapi.Outputter = (*FileList)(nil)
