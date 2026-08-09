package tools

// office_extract reads Office Open XML documents — Word (.docx), PowerPoint
// (.pptx), and Excel (.xlsx) — into plain text. These formats are all just a ZIP
// of XML parts, so extraction is pure standard library (archive/zip +
// encoding/xml): NO third-party dependency, which is why this is a built-in tool
// and not a build-tag plugin like pdf (whose PDF library has to be kept out of
// the default binary). Legacy binary formats (.doc/.ppt/.xls) are intentionally
// out of scope — those are an OLE binary container that needs a heavy converter;
// "Save As" to the modern format is the fix.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	agenttools "github.com/Compdeep/kaiju/agent/tools"
)

// officeMaxChars caps returned text so a huge document can't blow the context
// window. The caller can raise it per call.
const officeMaxChars = 200_000

// OOXML content types, keyed for both the upload allowlist and web_fetch decoders.
const (
	mimeDocx = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	mimePptx = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	mimeXlsx = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
)

// OfficeExtract is the office_extract tool: text out of .docx/.pptx/.xlsx.
type OfficeExtract struct{ workspace string }

// NewOfficeExtract returns the office_extract tool, sandboxed to workspace.
func NewOfficeExtract(workspace string) *OfficeExtract { return &OfficeExtract{workspace: workspace} }

var _ agenttools.Tool = (*OfficeExtract)(nil)

func (t *OfficeExtract) Name() string { return "office_extract" }

func (t *OfficeExtract) Description() string {
	return "Extract the text of a Word (.docx), PowerPoint (.pptx), or Excel (.xlsx) file into plain text. Use this to read an uploaded Office document — slides, paragraphs, or cells — before answering questions about it. Input is a file path. Legacy binary .doc/.ppt/.xls are NOT supported (save them as the modern .docx/.pptx/.xlsx first)."
}

func (t *OfficeExtract) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "Path to the .docx, .pptx, or .xlsx file to read."},
			"max_chars": {"type": "integer", "description": "Optional cap on returned characters (default 200000)."}
		},
		"required": ["path"]
	}`)
}

// Impact is observe-only: it opens a file read-only and returns its text.
func (t *OfficeExtract) Impact(map[string]any) int { return agenttools.ImpactObserve }

// Execute satisfies the Tool interface for callers outside the DAG.
func (t *OfficeExtract) Execute(ctx context.Context, params map[string]any) (string, error) {
	return agenttools.StringResult(t.ExecuteTyped(ctx, params))
}

func (t *OfficeExtract) ExecuteTyped(_ context.Context, params map[string]any) (agenttools.ToolMessage, error) {
	raw, _ := params["path"].(string)
	if strings.TrimSpace(raw) == "" {
		return agenttools.ToolMessage{}, fmt.Errorf("office_extract: 'path' is required")
	}
	path, err := t.resolve(raw)
	if err != nil {
		return agenttools.ToolMessage{}, err
	}
	maxChars := officeMaxChars
	if v, ok := params["max_chars"].(float64); ok && int(v) > 0 {
		maxChars = int(v)
	}

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".doc" || ext == ".ppt" || ext == ".xls" {
		return agenttools.ToolMessage{}, fmt.Errorf("office_extract: %q is the legacy binary format, which isn't supported — save it as .docx/.pptx/.xlsx and try again", filepath.Base(path))
	}

	zr, err := zip.OpenReader(path)
	if err != nil {
		return agenttools.ToolMessage{}, fmt.Errorf("office_extract: open %s: %w", filepath.Base(path), err)
	}
	defer zr.Close()

	kind, out, err := extractOOXML(&zr.Reader, ext, maxChars)
	if err != nil {
		return agenttools.ToolMessage{}, fmt.Errorf("office_extract: %s: %w", filepath.Base(path), err)
	}
	// The document opened and held no text — a scanned page, an empty deck. It
	// used to say so in a sentence the model had to read and believe; now the
	// coverage statement carries it.
	if strings.TrimSpace(out) == "" {
		return agenttools.ToolEmpty("text", "opened "+filepath.Base(path)+" but found no extractable text"), nil
	}
	if len(out) > maxChars {
		out = out[:maxChars] + "\n…[truncated]"
	}
	return agenttools.ToolOK("text", fmt.Sprintf("%s: %s\n\n%s", kind, filepath.Base(path), out),
		map[string]any{"kind": kind, "file": filepath.Base(path)}), nil
}

// extractOOXML dispatches to the right family. When ext is empty/unknown it
// sniffs the zip's part layout (the path web_fetch takes with only a MIME). It
// returns a human label ("Word"/"PowerPoint"/"Excel") and the text.
func extractOOXML(zr *zip.Reader, ext string, maxChars int) (kind, text string, err error) {
	switch {
	case ext == ".docx" || (ext == "" && hasFile(zr, "word/document.xml")):
		return "Word", extractDocx(zr, maxChars), nil
	case ext == ".pptx" || (ext == "" && hasPrefix(zr, "ppt/slides/slide")):
		return "PowerPoint", extractPptx(zr, maxChars), nil
	case ext == ".xlsx" || (ext == "" && hasFile(zr, "xl/workbook.xml")):
		return "Excel", extractXlsx(zr, maxChars), nil
	default:
		return "", "", fmt.Errorf("unrecognised Office file (expected .docx/.pptx/.xlsx)")
	}
}

// ── Word (.docx) ──────────────────────────────────────────────────────────────

func extractDocx(zr *zip.Reader, maxChars int) string {
	f := openFile(zr, "word/document.xml")
	if f == nil {
		return ""
	}
	defer f.Close()
	// Paragraphs (<w:p>) become line breaks; <w:t> holds the text.
	return strings.TrimSpace(pullText(f, map[string]bool{"p": true}, maxChars))
}

// ── PowerPoint (.pptx) ────────────────────────────────────────────────────────

func extractPptx(zr *zip.Reader, maxChars int) string {
	var slides []*zip.File
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			slides = append(slides, f)
		}
	}
	sort.Slice(slides, func(i, j int) bool { return slideNum(slides[i].Name) < slideNum(slides[j].Name) })

	var b strings.Builder
	for i, sf := range slides {
		rc, err := sf.Open()
		if err != nil {
			continue
		}
		// <a:p> paragraphs → line breaks; <a:t> holds the text.
		txt := strings.TrimSpace(pullText(rc, map[string]bool{"p": true}, maxChars))
		rc.Close()
		if txt == "" {
			continue
		}
		fmt.Fprintf(&b, "--- Slide %d ---\n%s\n\n", i+1, txt)
		if b.Len() >= maxChars {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// slideNum pulls the integer N out of ".../slideN.xml" for correct ordering
// (slide2 before slide10, which a lexical sort gets wrong).
func slideNum(name string) int {
	base := strings.TrimSuffix(filepath.Base(name), ".xml")
	base = strings.TrimPrefix(base, "slide")
	n, _ := strconv.Atoi(base)
	return n
}

// ── Excel (.xlsx) ─────────────────────────────────────────────────────────────

func extractXlsx(zr *zip.Reader, maxChars int) string {
	shared := readSharedStrings(zr)
	var sheets []*zip.File
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
			sheets = append(sheets, f)
		}
	}
	sort.Slice(sheets, func(i, j int) bool { return sheets[i].Name < sheets[j].Name })

	var b strings.Builder
	for i, sf := range sheets {
		rc, err := sf.Open()
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "--- Sheet %d ---\n", i+1)
		writeSheet(&b, rc, shared, maxChars)
		rc.Close()
		b.WriteByte('\n')
		if b.Len() >= maxChars {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// readSharedStrings loads xl/sharedStrings.xml — the string pool xlsx cells refer
// to by index. Each <si> is one string (its <t> runs joined).
func readSharedStrings(zr *zip.Reader) []string {
	f := openFile(zr, "xl/sharedStrings.xml")
	if f == nil {
		return nil
	}
	defer f.Close()
	dec := xml.NewDecoder(f)
	var out []string
	var cur strings.Builder
	inSi, inT := false, false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSi = true
				cur.Reset()
			case "t":
				inT = true
			}
		case xml.CharData:
			if inSi && inT {
				cur.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "si":
				out = append(out, cur.String())
				inSi = false
			}
		}
	}
	return out
}

// writeSheet streams one worksheet, emitting rows as tab-separated cell values.
// Shared-string cells (t="s") resolve their <v> index against shared; inline
// strings (t="inlineStr") and numbers use their value directly.
func writeSheet(b *strings.Builder, r io.Reader, shared []string, maxChars int) {
	dec := xml.NewDecoder(r)
	var cellType string
	var cellVal strings.Builder
	inVal := false
	var row []string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "c":
				cellType = ""
				for _, a := range t.Attr {
					if a.Name.Local == "t" {
						cellType = a.Value
					}
				}
				cellVal.Reset()
			case "v", "t": // <v> = value/shared-index; <t> = inline-string text
				inVal = true
			}
		case xml.CharData:
			if inVal {
				cellVal.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "t":
				inVal = false
			case "c":
				val := cellVal.String()
				if cellType == "s" {
					if idx, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && idx >= 0 && idx < len(shared) {
						val = shared[idx]
					}
				}
				row = append(row, val)
			case "row":
				b.WriteString(strings.Join(row, "\t"))
				b.WriteByte('\n')
				row = row[:0]
			}
		}
		if b.Len() >= maxChars {
			break
		}
	}
}

// ── shared XML text puller ────────────────────────────────────────────────────

// pullText streams an XML part, appending the character data inside every <*:t>
// element (namespace-agnostic — w:t in Word, a:t in PowerPoint). A closing
// element whose local name is in breakOn appends a newline (paragraphs), and
// <*:tab>/<*:br> become tab/newline. Capped at maxChars.
func pullText(r io.Reader, breakOn map[string]bool, maxChars int) string {
	dec := xml.NewDecoder(r)
	var b strings.Builder
	inT := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inT = true
			case "tab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			}
		case xml.CharData:
			if inT {
				b.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inT = false
			}
			if breakOn[t.Name.Local] {
				b.WriteByte('\n')
			}
		}
		if b.Len() >= maxChars {
			break
		}
	}
	return b.String()
}

// ── web_fetch binary decoder ──────────────────────────────────────────────────

// decodeOfficeBytes extracts text from raw OOXML bytes web_fetch downloaded. It
// sniffs the family from the zip's part layout, so one decoder serves all three
// MIMEs. Empty return ⇒ no extractable text (web_fetch says so, doesn't invent).
func decodeOfficeBytes(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("office decode: %w", err)
	}
	kind, out, err := extractOOXML(zr, "", officeMaxChars)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "", nil
	}
	return fmt.Sprintf("%s document\n\n%s", kind, out), nil
}

// RegisterOfficeDecoders teaches core web_fetch to read the three OOXML MIMEs a
// search turns up (a linked .docx/.pptx/.xlsx), the same seam the pdf plugin uses
// for application/pdf. Call once at startup.
func RegisterOfficeDecoders() {
	agenttools.RegisterBinaryDecoder(mimeDocx, decodeOfficeBytes)
	agenttools.RegisterBinaryDecoder(mimePptx, decodeOfficeBytes)
	agenttools.RegisterBinaryDecoder(mimeXlsx, decodeOfficeBytes)
}

// ── zip + sandbox helpers ─────────────────────────────────────────────────────

func openFile(zr *zip.Reader, name string) io.ReadCloser {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil
			}
			return rc
		}
	}
	return nil
}

func hasFile(zr *zip.Reader, name string) bool {
	for _, f := range zr.File {
		if f.Name == name {
			return true
		}
	}
	return false
}

func hasPrefix(zr *zip.Reader, prefix string) bool {
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, prefix) {
			return true
		}
	}
	return false
}

// resolve keeps file access inside the workspace sandbox, mirroring pdf_extract:
// a relative path joins the workspace; an absolute path must live under it.
func (t *OfficeExtract) resolve(p string) (string, error) {
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
		return "", fmt.Errorf("office_extract: path escapes workspace: %s", p)
	}
	return abs, nil
}
