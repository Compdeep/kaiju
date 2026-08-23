package config

import (
	"os"
	"path/filepath"
	"testing"
)

// kaiju's own interface is the whole one. An application embedding it gets the
// opposite default; this is the line that separates the two, so it is worth a
// test that fails if anyone removes it.
func TestDefault_EverySectionOn(t *testing.T) {
	c := Default()
	if !c.UI.Sections.Users || !c.UI.Sections.Workspace {
		t.Fatalf("kaiju's own default left a section off: %+v", c.UI.Sections)
	}
}

// Defaults are built first and the file is unmarshalled over them, so naming
// one section must not silently switch off the ones the file does not mention.
func TestLoad_NamingOneSectionLeavesTheOthers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kaiju.json")
	if err := os.WriteFile(path, []byte(`{"ui":{"sections":{"users":false}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.UI.Sections.Users {
		t.Error("the file switched users off and it is still on")
	}
	if !c.UI.Sections.Workspace {
		t.Error("naming one section switched off another the file never mentioned")
	}
}

func TestLoad_ABrandAndAThemeArriveIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kaiju.json")
	body := `{"ui":{"brand":{"name":"Acme","attribution":true},
	                 "theme":{"default":"dark","light":{"--accent":"#2F6FED"}}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.UI.Brand.Name != "Acme" || !c.UI.Brand.Attribution {
		t.Errorf("brand = %+v", c.UI.Brand)
	}
	if c.UI.Theme.Default != "dark" || c.UI.Theme.Light["--accent"] != "#2F6FED" {
		t.Errorf("theme = %+v", c.UI.Theme)
	}
	if err := c.UI.Validate(); err != nil {
		t.Errorf("a configuration from a file was refused by Validate: %v", err)
	}
}
