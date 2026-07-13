// Package uploads handles user-uploaded files: validation, storage,
// metadata extraction, optional LLM summarisation, and memory entry.
//
// The pipeline is synchronous — the upload HTTP request blocks until
// the file is on disk, sidecars are written, and the memory entry is
// recorded. Frontend chip stays in "uploading…" state for the full
// duration, then jumps straight to "✓".
//
// Files live under <workspace>/uploads/<session-id>/. Sidecars are
// named <file>.meta.json (always) and <file>.summary.md (when text >
// SummaryThreshold). Workspace SafeJoin is enforced so uploads cannot
// escape the workspace root.
package uploads

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/internal/agent"
	"github.com/Compdeep/kaiju/internal/agent/llm"
	"github.com/Compdeep/kaiju/internal/workspace"
)

// Limits — defaults; future config wiring can override.
const (
	MaxFileSize      = 25 * 1024 * 1024  // 25 MB per file
	MaxSessionTotal  = 200 * 1024 * 1024 // 200 MB per session (enforced at upload time)
	InlineThreshold  = 8 * 1024          // ≤ 8 KB → frontend inlines content into query
	SummaryThreshold = 100 * 1024        // > 100 KB text-class → LLM summary written
	PreviewLines     = 50                // head/tail line count in meta.json for plain text
	CSVSampleRows    = 10                // sample rows for CSV/TSV meta
)

// allowedExt is the MIME-by-extension allowlist. Anything not here is
// rejected at validation time.
var allowedExt = map[string]string{
	".txt": "text/plain", ".md": "text/markdown", ".log": "text/plain",
	".csv":  "text/csv",
	".tsv":  "text/tab-separated-values",
	".json": "application/json", ".jsonl": "application/x-ndjson",
	".yaml": "text/yaml", ".yml": "text/yaml",
	".xml": "text/xml", ".html": "text/html", ".htm": "text/html",
	".py": "text/x-python", ".js": "text/javascript", ".ts": "text/typescript",
	".jsx": "text/javascript", ".tsx": "text/typescript",
	".go": "text/x-go", ".rs": "text/x-rust", ".java": "text/x-java",
	".c": "text/x-c", ".cpp": "text/x-c++", ".h": "text/x-c", ".hpp": "text/x-c++",
	".sh": "text/x-shellscript", ".rb": "text/x-ruby", ".php": "text/x-php",
	".sql": "application/sql",
	".pdf": "application/pdf",
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp",
}

// Result is the JSON response payload for a successful upload — the
// shape the frontend chip needs to render and the agent needs to
// reference the file in subsequent queries.
type Result struct {
	Filename    string `json:"filename"`     // sanitised basename
	Path        string `json:"path"`         // workspace-relative — what the agent sees
	Type        string `json:"type"`         // mime as classified by allowedExt
	Size        int64  `json:"size"`         // bytes
	Lines       int    `json:"lines,omitempty"`
	MetaPath    string `json:"meta_path,omitempty"`
	SummaryPath string `json:"summary_path,omitempty"`
	Inline      string `json:"inline,omitempty"` // full content if size ≤ InlineThreshold
	UploadedAt  string `json:"uploaded_at"`
}

// Meta is the JSON sidecar persisted next to the file. Captures the
// preview the agent uses to decide whether it needs to read the full
// file. Schema deliberately permissive — different file types fill
// different fields.
type Meta struct {
	Filename    string         `json:"filename"`
	Type        string         `json:"type"`
	Size        int64          `json:"size"`
	Lines       int            `json:"lines,omitempty"`
	HeadLines   []string       `json:"head_lines,omitempty"`
	TailLines   []string       `json:"tail_lines,omitempty"`
	CSVColumns  []string       `json:"csv_columns,omitempty"`
	CSVRowCount int            `json:"csv_row_count,omitempty"`
	CSVSample   [][]string     `json:"csv_sample,omitempty"`
	JSONSchema  map[string]any `json:"json_schema,omitempty"`
	JSONSample  []any          `json:"json_sample,omitempty"`
	UploadedAt  string         `json:"uploaded_at"`
}

// Processor wraps the synchronous pipeline. Constructed once at startup
// with references to the agent (for memory + workspace) and the
// executor LLM client (for summary). Stateless beyond those refs.
type Processor struct {
	agent    *agent.Agent
	executor *llm.Client // may be nil; summary is then skipped
}

// New builds a Processor. Call once at startup and reuse.
func New(ag *agent.Agent, executor *llm.Client) *Processor {
	return &Processor{agent: ag, executor: executor}
}

// SessionImageDataURIs returns every image a session has uploaded, each encoded
// as a base64 data: URI ready to hand to a vision model. Empty on none/error.
// This is what makes an uploaded image "pinned": it's re-read each turn, so the
// image stays visible across follow-up questions.
func (p *Processor) SessionImageDataURIs(sessionID string) []string {
	if sessionID == "" {
		return nil
	}
	dir := filepath.Join(p.agent.Workspace(), "uploads", sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		mime, ok := allowedExt[strings.ToLower(filepath.Ext(e.Name()))]
		if !ok || !strings.HasPrefix(mime, "image/") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil || len(data) == 0 {
			continue
		}
		out = append(out, "data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(data))
	}
	return out
}

// Process runs the full pipeline for one upload: validate → write →
// extract metadata → optional summarise → memory entry. Returns the
// Result the HTTP handler hands back to the frontend.
//
// sessionID determines the destination directory (uploads/<sid>/) and
// the memory tag (session:<sid>). reader streams the file content.
// Bytes is the declared content length from the multipart header — used
// for early size rejection before reading; the actual on-disk size is
// computed from io.Copy and validated again.
func (p *Processor) Process(ctx context.Context, sessionID, filename string, declaredSize int64, reader io.Reader) (*Result, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session id required")
	}
	clean := sanitizeFilename(filename)
	if clean == "" {
		return nil, fmt.Errorf("invalid filename")
	}
	ext := strings.ToLower(filepath.Ext(clean))
	mimeType, ok := allowedExt[ext]
	if !ok {
		return nil, fmt.Errorf("file type %q not in allowlist", ext)
	}
	if declaredSize > MaxFileSize {
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", declaredSize, MaxFileSize)
	}

	uploadsRoot := filepath.Join(p.agent.Workspace(), "uploads", sessionID)
	if err := os.MkdirAll(uploadsRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create uploads dir: %w", err)
	}

	if err := p.checkSessionQuota(uploadsRoot, declaredSize); err != nil {
		return nil, err
	}

	destAbs, err := workspace.SafeJoin(p.agent.Workspace(), filepath.Join("uploads", sessionID, clean))
	if err != nil {
		return nil, fmt.Errorf("safejoin: %w", err)
	}

	// Stream to disk with a byte cap so a lying Content-Length header
	// can't blow past MaxFileSize.
	out, err := os.Create(destAbs)
	if err != nil {
		return nil, fmt.Errorf("create file: %w", err)
	}
	written, copyErr := io.Copy(out, io.LimitReader(reader, MaxFileSize+1))
	closeErr := out.Close()
	if copyErr != nil {
		os.Remove(destAbs)
		return nil, fmt.Errorf("write: %w", copyErr)
	}
	if closeErr != nil {
		os.Remove(destAbs)
		return nil, fmt.Errorf("close: %w", closeErr)
	}
	if written > MaxFileSize {
		os.Remove(destAbs)
		return nil, fmt.Errorf("file exceeded MaxFileSize during stream (%d > %d)", written, MaxFileSize)
	}

	relPath := filepath.Join("uploads", sessionID, clean)
	now := time.Now().UTC().Format(time.RFC3339)

	meta := &Meta{
		Filename:   clean,
		Type:       mimeType,
		Size:       written,
		UploadedAt: now,
	}
	// Type-specific extractor populates the rest of meta.
	if err := extractMeta(destAbs, mimeType, meta); err != nil {
		// Extraction failure is non-fatal — file is still on disk and
		// agent can read it raw. Log via meta and proceed.
		meta.HeadLines = []string{fmt.Sprintf("(metadata extraction failed: %v)", err)}
	}

	metaPath := destAbs + ".meta.json"
	if err := writeJSON(metaPath, meta); err != nil {
		return nil, fmt.Errorf("write meta: %w", err)
	}
	relMeta := relPath + ".meta.json"

	res := &Result{
		Filename:   clean,
		Path:       relPath,
		Type:       mimeType,
		Size:       written,
		Lines:      meta.Lines,
		MetaPath:   relMeta,
		UploadedAt: now,
	}

	// Inline tiny files for zero-round-trip access in queries.
	if written <= InlineThreshold && isTextish(mimeType) {
		if data, err := os.ReadFile(destAbs); err == nil {
			res.Inline = string(data)
		}
	}

	// Summary for big text-class files.
	if written > SummaryThreshold && isTextish(mimeType) && p.executor != nil {
		summaryPath := destAbs + ".summary.md"
		relSummary := relPath + ".summary.md"
		if err := p.summarise(ctx, destAbs, summaryPath); err != nil {
			// Summary failure is non-fatal — chip still becomes "ready"
			// with just the metadata sidecar. Log and move on.
			fmt.Fprintf(os.Stderr, "[uploads] summary failed for %s: %v\n", relPath, err)
		} else {
			res.SummaryPath = relSummary
		}
	}

	// Memory entry — keyed deterministically so re-uploading the same
	// filename in the same session overwrites cleanly.
	if mem := p.agent.Memory(); mem != nil {
		key := fmt.Sprintf("upload:%s:%s", sessionID, clean)
		entry := map[string]any{
			"path":         relPath,
			"meta":         relMeta,
			"summary":      res.SummaryPath,
			"type":         mimeType,
			"size_bytes":   written,
			"lines":        meta.Lines,
			"uploaded_at":  now,
			"session_id":   sessionID,
		}
		if b, err := json.Marshal(entry); err == nil {
			tags := []string{"upload", "session:" + sessionID, "type:" + simplifyMime(mimeType)}
			mem.Set(key, string(b), 0, tags) // ttl=0 → no expiry
		}
	}

	return res, nil
}

// Delete removes a session upload (file + sidecars) and its memory
// entry. Returns an error only if the file path can't be resolved
// safely; missing files are treated as success.
func (p *Processor) Delete(sessionID, filename string) error {
	clean := sanitizeFilename(filename)
	if clean == "" {
		return fmt.Errorf("invalid filename")
	}
	destAbs, err := workspace.SafeJoin(p.agent.Workspace(), filepath.Join("uploads", sessionID, clean))
	if err != nil {
		return fmt.Errorf("safejoin: %w", err)
	}
	for _, suffix := range []string{"", ".meta.json", ".summary.md"} {
		_ = os.Remove(destAbs + suffix)
	}
	if mem := p.agent.Memory(); mem != nil {
		key := fmt.Sprintf("upload:%s:%s", sessionID, clean)
		mem.Delete(key)
	}
	return nil
}

// List returns the Result records for every upload in a session, by
// reading the .meta.json sidecars under uploads/<sid>/. Used to
// restore the chip strip when a session is reloaded.
func (p *Processor) List(sessionID string) ([]*Result, error) {
	root := filepath.Join(p.agent.Workspace(), "uploads", sessionID)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Result{}, nil
		}
		return nil, err
	}
	var out []*Result
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".meta.json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			continue
		}
		var meta Meta
		if err := json.Unmarshal(raw, &meta); err != nil {
			continue
		}
		relPath := filepath.Join("uploads", sessionID, meta.Filename)
		res := &Result{
			Filename:   meta.Filename,
			Path:       relPath,
			Type:       meta.Type,
			Size:       meta.Size,
			Lines:      meta.Lines,
			MetaPath:   relPath + ".meta.json",
			UploadedAt: meta.UploadedAt,
		}
		summaryAbs := filepath.Join(p.agent.Workspace(), relPath+".summary.md")
		if _, err := os.Stat(summaryAbs); err == nil {
			res.SummaryPath = relPath + ".summary.md"
		}
		out = append(out, res)
	}
	return out, nil
}

// checkSessionQuota walks the session's uploads dir and returns an
// error if the existing total + the incoming file would exceed
// MaxSessionTotal. The walk is fast for the small file counts we
// expect; revisit if a session ever has thousands.
func (p *Processor) checkSessionQuota(dir string, incoming int64) error {
	var total int64
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // fresh dir, no quota issue
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	if total+incoming > MaxSessionTotal {
		return fmt.Errorf("session upload quota exceeded: %d + %d > %d", total, incoming, MaxSessionTotal)
	}
	return nil
}

// sanitizeFilename strips path components and dangerous characters,
// keeping a single safe basename. Empty result means rejected.
func sanitizeFilename(s string) string {
	s = filepath.Base(s)
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." {
		return ""
	}
	// Remove any control chars, leading dots that aren't extensions.
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == '/' || c == '\\' {
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

// isTextish reports whether the mime should be treated as text for
// inlining and summarisation purposes.
func isTextish(mime string) bool {
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch mime {
	case "application/json", "application/x-ndjson", "application/sql":
		return true
	}
	return false
}

// simplifyMime collapses a full mime to a short tag suffix for the
// memory entry. Keeps tag values readable in admin tooling.
func simplifyMime(mime string) string {
	if i := strings.IndexByte(mime, '/'); i >= 0 {
		return mime[i+1:]
	}
	return mime
}

// writeJSON marshals v as indented JSON and writes it to path.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
