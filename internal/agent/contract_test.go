package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── Path resolution ──

func TestInterfacesPath(t *testing.T) {
	p := interfacesPath("/tmp/workspace")
	want := "/tmp/workspace/blueprints/interfaces.json"
	if p != want {
		t.Errorf("path = %q, want %q", p, want)
	}
}

// ── Load behavior ──

func TestLoadInterfaces_MissingFile(t *testing.T) {
	dir := t.TempDir()
	si := loadInterfaces(dir, "sess_abc")
	if si == nil {
		t.Fatal("loadInterfaces returned nil for missing file")
	}
	if len(si.Interfaces) != 0 || len(si.Schema) != 0 {
		t.Errorf("missing file should return empty interfaces, got %+v", si)
	}
}

func TestLoadInterfaces_EmptySession(t *testing.T) {
	dir := t.TempDir()
	si := loadInterfaces(dir, "")
	if si == nil {
		t.Fatal("empty session should return non-nil empty interfaces")
	}
}

func TestLoadInterfaces_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "blueprints"), 0755)
	seed := `{
		"sess_abc": {
			"interfaces": {"POST /login": {"req": "email+pass", "res": "token"}},
			"schema": {"users": "id, email, hash"}
		}
	}`
	os.WriteFile(filepath.Join(dir, "blueprints", "interfaces.json"), []byte(seed), 0644)

	si := loadInterfaces(dir, "sess_abc")
	if _, ok := si.Interfaces["POST /login"]; !ok {
		t.Error("interface POST /login missing after load")
	}
	if _, ok := si.Schema["users"]; !ok {
		t.Error("schema users missing after load")
	}
}

func TestLoadInterfaces_WrongSession(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "blueprints"), 0755)
	seed := `{"sess_abc": {"interfaces": {"POST /login": "yes"}}}`
	os.WriteFile(filepath.Join(dir, "blueprints", "interfaces.json"), []byte(seed), 0644)

	si := loadInterfaces(dir, "sess_other")
	if len(si.Interfaces) != 0 {
		t.Error("wrong session should return empty interfaces")
	}
}

// ── Save behavior ──

func TestSaveInterfaces_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	si := &sessionInterfaces{
		Interfaces: map[string]any{"k": "v"},
		Schema:     map[string]any{},
	}
	if err := saveInterfaces(dir, "sess_1", si); err != nil {
		t.Fatalf("save: %v", err)
	}
	path := interfacesPath(dir)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestSaveInterfaces_EmptySessionNoop(t *testing.T) {
	dir := t.TempDir()
	if err := saveInterfaces(dir, "", &sessionInterfaces{}); err != nil {
		t.Errorf("empty session should no-op, got error: %v", err)
	}
}

func TestSaveInterfaces_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	original := &sessionInterfaces{
		Interfaces: map[string]any{
			"POST /api/auth/login": map[string]any{
				"request":  map[string]any{"email": "string"},
				"response": map[string]any{"token": "string"},
			},
		},
		Schema: map[string]any{"users": "id integer primary key, email text"},
	}
	if err := saveInterfaces(dir, "sess_rt", original); err != nil {
		t.Fatal(err)
	}
	reloaded := loadInterfaces(dir, "sess_rt")
	if _, ok := reloaded.Interfaces["POST /api/auth/login"]; !ok {
		t.Error("interface key lost on roundtrip")
	}
	if reloaded.Schema["users"] != original.Schema["users"] {
		t.Errorf("schema lost: %v vs %v", reloaded.Schema["users"], original.Schema["users"])
	}
}

// ── Merge behavior (additive) ──

func TestSaveInterfaces_MergesAdditively(t *testing.T) {
	dir := t.TempDir()

	// Turn 1
	si1 := &sessionInterfaces{
		Interfaces: map[string]any{"POST /login": "v1"},
		Schema:     map[string]any{"users": "id, email"},
	}
	saveInterfaces(dir, "sess_m", si1)

	// Turn 2 — adds new interface, doesn't touch existing
	si2 := &sessionInterfaces{
		Interfaces: map[string]any{"POST /register": "v1"},
		Schema:     map[string]any{},
	}
	saveInterfaces(dir, "sess_m", si2)

	// Reload — should have both
	reloaded := loadInterfaces(dir, "sess_m")
	if _, ok := reloaded.Interfaces["POST /login"]; !ok {
		t.Error("existing interface was dropped")
	}
	if _, ok := reloaded.Interfaces["POST /register"]; !ok {
		t.Error("new interface was not added")
	}
	if _, ok := reloaded.Schema["users"]; !ok {
		t.Error("existing schema was dropped")
	}
}

func TestSaveInterfaces_OverwritesSameKey(t *testing.T) {
	dir := t.TempDir()
	saveInterfaces(dir, "sess_o", &sessionInterfaces{
		Interfaces: map[string]any{"POST /login": "old"},
	})
	saveInterfaces(dir, "sess_o", &sessionInterfaces{
		Interfaces: map[string]any{"POST /login": "new"},
	})
	reloaded := loadInterfaces(dir, "sess_o")
	if reloaded.Interfaces["POST /login"] != "new" {
		t.Errorf("supersede failed: %v", reloaded.Interfaces["POST /login"])
	}
}

// ── Session isolation ──

func TestSaveInterfaces_SessionsIsolated(t *testing.T) {
	dir := t.TempDir()
	saveInterfaces(dir, "sess_A", &sessionInterfaces{
		Interfaces: map[string]any{"A_iface": "a"},
	})
	saveInterfaces(dir, "sess_B", &sessionInterfaces{
		Interfaces: map[string]any{"B_iface": "b"},
	})

	a := loadInterfaces(dir, "sess_A")
	if _, ok := a.Interfaces["B_iface"]; ok {
		t.Error("session A leaked session B interface")
	}
	if _, ok := a.Interfaces["A_iface"]; !ok {
		t.Error("session A lost its own interface")
	}
}

// ── Prompt formatting ──

func TestFormatInterfacesForPrompt_Empty(t *testing.T) {
	si := &sessionInterfaces{}
	if s := formatInterfacesForPrompt(si); s != "" {
		t.Errorf("empty interfaces should format to empty string, got: %q", s)
	}
}

func TestFormatInterfacesForPrompt_WithContent(t *testing.T) {
	si := &sessionInterfaces{
		Interfaces: map[string]any{"POST /login": "foo"},
		Schema:     map[string]any{"users": "bar"},
	}
	s := formatInterfacesForPrompt(si)
	if !strings.Contains(s, "Existing Interfaces") {
		t.Error("output missing heading")
	}
	if !strings.Contains(s, "POST /login") {
		t.Error("output missing interface")
	}
	if !strings.Contains(s, "users") {
		t.Error("output missing schema")
	}
}

func TestFormatInterfacesForPrompt_NilSafe(t *testing.T) {
	if s := formatInterfacesForPrompt(nil); s != "" {
		t.Errorf("nil interfaces should format to empty string, got: %q", s)
	}
}

// ── End-to-end: multi-turn merge ──

func TestInterfaces_E2EMultiTurn(t *testing.T) {
	dir := t.TempDir()

	// Turn 1: no interfaces exist
	si := loadInterfaces(dir, "sess_e2e")
	if len(si.Interfaces) != 0 {
		t.Fatal("first load should be empty")
	}

	// Architect emits initial interfaces and schema
	saveInterfaces(dir, "sess_e2e", &sessionInterfaces{
		Interfaces: map[string]any{
			"POST /api/login":    "email+password -> token",
			"POST /api/register": "email+password+name -> token",
		},
		Schema: map[string]any{
			"users": "id integer primary key, email text unique",
		},
	})

	// Turn 2: architect adds OAuth
	saveInterfaces(dir, "sess_e2e", &sessionInterfaces{
		Interfaces: map[string]any{
			"POST /api/oauth": "provider+code -> token",
		},
	})

	// Turn 3: verify all three interfaces present
	si3 := loadInterfaces(dir, "sess_e2e")
	if len(si3.Interfaces) != 3 {
		t.Errorf("turn 3 should have 3 interfaces, got %d", len(si3.Interfaces))
	}
	for _, key := range []string{"POST /api/login", "POST /api/register", "POST /api/oauth"} {
		if _, ok := si3.Interfaces[key]; !ok {
			t.Errorf("key %q missing after multi-turn merge", key)
		}
	}
	if _, ok := si3.Schema["users"]; !ok {
		t.Error("schema from turn 1 lost")
	}
}

// ── Nested structures preserved ──

func TestInterfaces_NestedStructuresPreserved(t *testing.T) {
	dir := t.TempDir()
	original := &sessionInterfaces{
		Interfaces: map[string]any{
			"POST /api/auth/login": map[string]any{
				"request": map[string]any{
					"email":    "string",
					"password": "string",
				},
				"response": map[string]any{
					"token": "string",
					"user":  map[string]any{"id": "number", "email": "string"},
				},
			},
		},
		Schema: map[string]any{},
	}
	saveInterfaces(dir, "sess_nested", original)
	reloaded := loadInterfaces(dir, "sess_nested")

	origJSON, _ := json.Marshal(original.Interfaces)
	reloadedJSON, _ := json.Marshal(reloaded.Interfaces)
	if string(origJSON) != string(reloadedJSON) {
		t.Errorf("nested structure not preserved:\norig:     %s\nreloaded: %s", origJSON, reloadedJSON)
	}
}
