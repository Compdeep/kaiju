//go:build plugin_pdf

// Package pdf is an optional kaiju plugin that adds a `pdf_extract` tool for
// reading the text layer of text-based PDF files. Build it in with
// `-tags plugin_pdf` and switch it on with `plugins: ["pdf"]` in the config or
// `--plugins pdf` on the serve command.
//
// Scope: digital (text-layer) PDFs only. Scanned / image-only PDFs and Arabic
// reshaping are intentionally out of scope here — those belong to a future
// OCR/vision plugin. A scanned PDF simply comes back with little or no text, and
// the tool says so rather than failing.
package pdf

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
	"github.com/Compdeep/kaiju/internal/plugins"
	pdflib "github.com/ledongthuc/pdf"
)

func init() { plugins.Register(plugin{}) }

type plugin struct{}

func (plugin) Name() string { return "pdf" }

func (plugin) Tools(d plugins.Deps) []agenttools.Tool {
	return []agenttools.Tool{&extractTool{workspace: d.Workspace}}
}

// defaultMaxChars caps the returned text so a huge PDF can't blow up the context
// window. The planner can raise it per call.
const defaultMaxChars = 200_000

// extractTool reads a PDF's text layer and returns it as plain text.
type extractTool struct{ workspace string }

func (t *extractTool) Name() string { return "pdf_extract" }

func (t *extractTool) Description() string {
	return "Extract the text of a text-based PDF file into plain text. Use this to read the contents of a PDF — one that was uploaded, or one that web_fetch downloaded to disk — before answering questions about it. Input is a file path. Works on digital PDFs with a real text layer; a scanned or image-only PDF returns little or no text (that needs a vision model)."
}

func (t *extractTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Path to the .pdf file to read."},
			"max_chars": {"type": "integer", "description": "Optional cap on returned characters (default 200000)."}
		},
		"required": ["path"]
	}`)
}

// Impact is observe-only: it opens a file read-only and returns its text.
func (t *extractTool) Impact(map[string]any) int { return agenttools.ImpactObserve }

func (t *extractTool) Execute(_ context.Context, params map[string]any) (string, error) {
	raw, _ := params["path"].(string)
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("pdf_extract: 'path' is required")
	}
	path, err := t.resolve(raw)
	if err != nil {
		return "", err
	}
	maxChars := defaultMaxChars
	if v, ok := params["max_chars"].(float64); ok && int(v) > 0 {
		maxChars = int(v)
	}

	f, r, err := pdflib.Open(path)
	if err != nil {
		return "", fmt.Errorf("pdf_extract: open %s: %w", filepath.Base(path), err)
	}
	defer f.Close()

	var b strings.Builder
	pages := r.NumPage()
	for i := 1; i <= pages; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		b.WriteString(pageText(p))
		b.WriteByte('\n')
		if b.Len() >= maxChars {
			break
		}
	}

	out := strings.TrimSpace(b.String())
	if out == "" {
		return fmt.Sprintf("(pdf_extract read %d page(s) from %s but found no extractable text — it is likely a scanned or image-only PDF, which needs OCR or a vision model.)", pages, filepath.Base(path)), nil
	}
	if len(out) > maxChars {
		out = out[:maxChars] + "\n…[truncated]"
	}
	return fmt.Sprintf("PDF: %s (%d page(s))\n\n%s", filepath.Base(path), pages, out), nil
}

// pageText extracts a page's text with word spacing recovered from the glyph
// positions. ledongthuc's GetPlainText concatenates text runs with no spaces
// ("IndependentIntent-Gated…"), which reads badly for an LLM. GetTextByRow gives
// us each run's X position, so we can put a space back wherever there's a
// horizontal gap and a newline between rows. Falls back to GetPlainText if the
// row API yields nothing.
func pageText(p pdflib.Page) string {
	rows, err := p.GetTextByRow()
	if err != nil || len(rows) == 0 {
		if txt, err := p.GetPlainText(nil); err == nil {
			return txt
		}
		return ""
	}
	var b strings.Builder
	endsSpace := true // start-of-buffer counts as "already spaced"
	for _, row := range rows {
		prevEnd := 0.0
		for i, tx := range row.Content {
			// Insert a space when this run starts a bit past where the previous
			// one ended — a real gap, not touching glyphs. Threshold scales with
			// font size (~a fifth of an em, min 1pt) so it adapts to zoom.
			if i > 0 {
				gap := tx.X - prevEnd
				thresh := tx.FontSize * 0.2
				if thresh < 1 {
					thresh = 1
				}
				if gap > thresh && !endsSpace && !strings.HasPrefix(tx.S, " ") {
					b.WriteByte(' ')
					endsSpace = true
				}
			}
			if tx.S != "" {
				b.WriteString(tx.S)
				endsSpace = strings.HasSuffix(tx.S, " ")
			}
			prevEnd = tx.X + tx.W
		}
		b.WriteByte('\n')
		endsSpace = true
	}
	return b.String()
}

// resolve keeps file access inside the workspace sandbox, mirroring file_read:
// a relative path joins the workspace; an absolute path must live under it.
func (t *extractTool) resolve(p string) (string, error) {
	if t.workspace == "" {
		return p, nil
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(t.workspace, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(t.workspace)
	if err != nil {
		return "", err
	}
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("pdf_extract: path escapes workspace: %s", p)
	}
	return abs, nil
}
