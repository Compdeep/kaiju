package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A PDF handed to office_extract must say so, and name the tool that reads one.
//
// The sentence explaining this existed, and sat after extractOOXML — which a
// non-ZIP never reaches, because it fails at the open above. So a PDF produced
// "zip: not a valid zip file", naming a container format the caller never
// mentioned, and one live run told a reader their two pitch decks were
// "corrupted, password-protected, or not actually PDFs". Both files were fine.
func TestOfficeExtract_APDFIsNamedAsAPDF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Investor Pitch Deck v4.pdf")
	// A real PDF header, and nothing a ZIP reader can open.
	if err := os.WriteFile(path, []byte("%PDF-1.7\n1 0 obj\n<< /Type /Catalog >>\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	msg := notOOXML(path)
	if !strings.Contains(msg, "pdf_extract") {
		t.Errorf("the message does not name the tool that can read it:\n  %s", msg)
	}
	if strings.Contains(msg, "zip") && !strings.Contains(msg, "PDF") {
		t.Errorf("the message names the container format and not the mistake:\n  %s", msg)
	}
	if !strings.Contains(msg, "Investor Pitch Deck v4.pdf") {
		t.Errorf("the message does not say which file:\n  %s", msg)
	}
}

// A legacy Office file says what to do about it, rather than mentioning ZIPs.
func TestOfficeExtract_ALegacyFormatSaysWhatToDo(t *testing.T) {
	msg := notOOXML("/tmp/report.doc")
	if !strings.Contains(msg, ".docx") {
		t.Errorf("the message does not say to save it as the modern format:\n  %s", msg)
	}
}

// Anything else points at file_read and pdf_extract without requiring the
// reader to know that Office documents are ZIP archives.
func TestOfficeExtract_AnythingElseNamesTheAlternatives(t *testing.T) {
	msg := notOOXML("/tmp/notes.txt")
	for _, want := range []string{"file_read", "pdf_extract"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not mention %s:\n  %s", want, msg)
		}
	}
}
