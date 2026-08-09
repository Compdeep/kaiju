package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Every migrated tool now declares the unified envelope schema (derived from
// EnvelopeSchema) — the declared contract matches the actual output shape.
func TestSchemaUnified(t *testing.T) {
	schemas := map[string]json.RawMessage{
		"sysinfo":    NewSysinfo().OutputSchema(),
		"file_read":  NewFileRead("").OutputSchema(),
		"file_write": NewFileWrite("").OutputSchema(),
		"file_list":  NewFileList("").OutputSchema(),
		"web_search": NewWebSearch().OutputSchema(),
		"web_fetch":  NewWebFetch().OutputSchema(),
		"env_list":   NewEnvList().OutputSchema(),
		"net_info":   NewNetInfo().OutputSchema(),
		"service":    (&Service{}).OutputSchema(),
	}
	for name, schema := range schemas {
		if !json.Valid(schema) {
			t.Errorf("%s OutputSchema is not valid JSON: %s", name, schema)
		}
		if !strings.Contains(string(schema), `"enum":["ok","empty","error"]`) {
			t.Errorf("%s OutputSchema not unified to the envelope: %s", name, schema)
		}
	}
	// structured tools keep their payload schema nested under data
	if !strings.Contains(string(NewWebFetch().OutputSchema()), `"data":`) {
		t.Errorf("web_fetch should carry its payload schema under data")
	}
	// text tools have no data — content-only
	if strings.Contains(string(NewFileRead("").OutputSchema()), `"data":`) {
		t.Errorf("file_read is text-only and should have no data schema")
	}
}

// mustEnvelope asserts a tool's Execute output is a well-formed ToolMessage.
func mustEnvelope(t *testing.T, out string, err error) toolapi.ToolMessage {
	t.Helper()
	if err != nil {
		t.Fatalf("Execute errored: %v", err)
	}
	m, ok := toolapi.ParseToolMessage(out)
	if !ok {
		t.Fatalf("output is not a ToolMessage envelope: %s", out)
	}
	return m
}

func TestSysinfo_EmitsEnvelope(t *testing.T) {
	out, err := NewSysinfo().Execute(nil, nil)
	m := mustEnvelope(t, out, err)
	if m.Type != "sysinfo" || m.Status != toolapi.StatusOK {
		t.Fatalf("sysinfo envelope = kind %q status %q", m.Type, m.Status)
	}
	if len(m.Data) == 0 {
		t.Fatalf("sysinfo should carry structured data")
	}
}

func TestFileRead_EmitsTextEnvelope(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := NewFileRead(dir).Execute(nil, map[string]any{"path": "f.txt"})
	m := mustEnvelope(t, out, err)
	if m.Type != "text" || m.Status != toolapi.StatusOK || m.Content != "hello world" {
		t.Fatalf("file_read envelope = kind %q status %q content %q", m.Type, m.Status, m.Content)
	}
}

// An empty file is a finding. It used to come back as ok with nothing in it,
// which reads to every consumer as a successful read and tells the coverage
// statement nothing.
func TestFileRead_EmptyFileIsReportedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nothing.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := NewFileRead(dir).Execute(nil, map[string]any{"path": "nothing.txt"})
	m := mustEnvelope(t, out, err)
	if m.Status != toolapi.StatusEmpty {
		t.Fatalf("status = %q, want empty", m.Status)
	}
	if !strings.Contains(m.Detail, "nothing.txt") {
		t.Fatalf("detail should name the file, got %q", m.Detail)
	}
}

// A missing file is a different outcome: the read fails and the node fails, so
// it must not be quietly reported as an empty file.
func TestFileRead_MissingFileStillErrors(t *testing.T) {
	if _, err := NewFileRead(t.TempDir()).Execute(nil, map[string]any{"path": "absent.txt"}); err == nil {
		t.Fatal("a missing file must return an error, not an empty envelope")
	}
}

func TestFileList_EmitsListingEnvelope(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	out, err := NewFileList(dir).Execute(nil, map[string]any{"path": "."})
	m := mustEnvelope(t, out, err)
	if m.Type != "listing" || m.Status != toolapi.StatusOK {
		t.Fatalf("file_list envelope = kind %q status %q", m.Type, m.Status)
	}
	if len(m.Data) == 0 {
		t.Fatalf("file_list should carry entries in data")
	}
}

func TestNetInfo_InterfacesEnvelope(t *testing.T) {
	out, err := NewNetInfo().Execute(nil, map[string]any{"action": "interfaces"})
	m := mustEnvelope(t, out, err)
	if m.Type != "net" || m.Status != toolapi.StatusOK {
		t.Fatalf("net_info envelope = kind %q status %q", m.Type, m.Status)
	}
	var d struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(m.Data, &d) != nil || d.Action != "interfaces" {
		t.Fatalf("net_info data.action = %q want interfaces (%s)", d.Action, m.Data)
	}
}

// The audit's silent-miss / no-match cases must now surface as StatusEmpty — the
// framing signal the coverage edge reads.
func TestEnvList_NoMatchIsEmpty(t *testing.T) {
	out, err := NewEnvList().Execute(nil, map[string]any{"filter": "ZZ_NO_SUCH_VAR_XYZ_"})
	m := mustEnvelope(t, out, err)
	if m.Status != toolapi.StatusEmpty {
		t.Fatalf("env_list with no matches should be StatusEmpty, got %q", m.Status)
	}
}
