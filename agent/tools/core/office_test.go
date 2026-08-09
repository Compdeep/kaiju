package core

import (
	"archive/zip"
	"context"
	agenttools "github.com/Compdeep/kaiju/agent/tools"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeZip builds a .zip at path from a name→content map (the OOXML parts).
func writeZip(t *testing.T, path string, parts map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// runExtract returns the readable text the model would see, not the envelope
// around it — the tests are about what was extracted from the document.
func runExtract(t *testing.T, dir, file string) string {
	t.Helper()
	return runExtractMsg(t, dir, file).Content
}

func runExtractMsg(t *testing.T, dir, file string) agenttools.ToolMessage {
	t.Helper()
	msg, err := NewOfficeExtract(dir).ExecuteTyped(context.Background(), map[string]any{"path": file})
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return msg
}

func TestOfficeExtract_Docx(t *testing.T) {
	dir := t.TempDir()
	writeZip(t, filepath.Join(dir, "doc.docx"), map[string]string{
		"word/document.xml": `<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>Hello Docx Paragraph One</w:t></w:r></w:p>
<w:p><w:r><w:t>Second </w:t></w:r><w:r><w:t>Paragraph</w:t></w:r></w:p>
</w:body></w:document>`,
	})
	out := runExtract(t, dir, "doc.docx")
	if !strings.Contains(out, "Hello Docx Paragraph One") || !strings.Contains(out, "Second Paragraph") {
		t.Fatalf("docx text missing:\n%s", out)
	}
	if !strings.HasPrefix(out, "Word:") {
		t.Fatalf("expected Word label, got:\n%s", out)
	}
}

func TestOfficeExtract_Pptx(t *testing.T) {
	dir := t.TempDir()
	// slide10 before slide2 lexically — proves numeric ordering.
	writeZip(t, filepath.Join(dir, "deck.pptx"), map[string]string{
		"ppt/slides/slide1.xml":  slideXML("First Slide"),
		"ppt/slides/slide2.xml":  slideXML("Second Slide"),
		"ppt/slides/slide10.xml": slideXML("Tenth Slide"),
	})
	out := runExtract(t, dir, "deck.pptx")
	for _, want := range []string{"First Slide", "Second Slide", "Tenth Slide", "--- Slide 1 ---", "--- Slide 3 ---"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pptx missing %q:\n%s", want, out)
		}
	}
	// slide2 must come before slide10 in the output (numeric order).
	if strings.Index(out, "Second Slide") > strings.Index(out, "Tenth Slide") {
		t.Fatalf("slides out of order:\n%s", out)
	}
}

func slideXML(text string) string {
	return `<?xml version="1.0"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
<a:p><a:r><a:t>` + text + `</a:t></a:r></a:p>
</p:sld>`
}

func TestOfficeExtract_Xlsx(t *testing.T) {
	dir := t.TempDir()
	writeZip(t, filepath.Join(dir, "book.xlsx"), map[string]string{
		"xl/workbook.xml": `<workbook/>`,
		"xl/sharedStrings.xml": `<?xml version="1.0"?>
<sst><si><t>Apple</t></si><si><t>Banana</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0"?>
<worksheet><sheetData>
<row><c t="s"><v>0</v></c><c t="s"><v>1</v></c></row>
<row><c><v>42</v></c></row>
</sheetData></worksheet>`,
	})
	out := runExtract(t, dir, "book.xlsx")
	// shared strings resolved, number kept, tab-separated row.
	if !strings.Contains(out, "Apple\tBanana") {
		t.Fatalf("xlsx shared-string row missing:\n%s", out)
	}
	if !strings.Contains(out, "42") {
		t.Fatalf("xlsx numeric cell missing:\n%s", out)
	}
}

func TestOfficeExtract_LegacyRejected(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "old.ppt"), []byte("binary junk"), 0644)
	tool := NewOfficeExtract(dir)
	_, err := tool.Execute(context.Background(), map[string]any{"path": "old.ppt"})
	if err == nil || !strings.Contains(err.Error(), "legacy binary") {
		t.Fatalf("expected legacy-binary rejection, got: %v", err)
	}
}

// A document that opens and holds no text is a finding, not a blank success.
// It used to say so in a sentence the model had to read and believe.
func TestOfficeExtract_NoTextIsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeZip(t, filepath.Join(dir, "blank.docx"), map[string]string{
		"word/document.xml": `<?xml version="1.0"?><w:document xmlns:w="x"><w:body></w:body></w:document>`,
	})
	msg := runExtractMsg(t, dir, "blank.docx")
	if msg.Status != agenttools.StatusEmpty {
		t.Fatalf("status = %q, want empty", msg.Status)
	}
	if !strings.Contains(msg.Detail, "blank.docx") {
		t.Fatalf("detail should name the file, got %q", msg.Detail)
	}
}
