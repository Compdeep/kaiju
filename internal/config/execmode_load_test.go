package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write a config file with the given agent.execution_mode and load it.
func loadWithExecMode(t *testing.T, mode string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "kaiju.json")
	body := map[string]any{"agent": map[string]any{"execution_mode": mode}}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

// A mistyped mode stops the daemon rather than running the other one. Read at
// use it compares unequal to "autonomous" and the node routes every turn — a
// working daemon doing the opposite of what its file asks, silently.
func TestAMistypedExecutionModeIsRefusedAtLoad(t *testing.T) {
	_, err := loadWithExecMode(t, "autonomus")
	if err == nil {
		t.Fatal("a mistyped execution_mode loaded")
	}
	for _, want := range []string{"autonomus", "execution_mode", "autonomous", "interactive"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

// Both real values load, and survive resolve() unchanged.
func TestTheRealExecutionModesLoad(t *testing.T) {
	for _, mode := range []string{"interactive", "autonomous"} {
		c, err := loadWithExecMode(t, mode)
		if err != nil {
			t.Fatalf("%q did not load: %v", mode, err)
		}
		if c.Agent.ExecutionMode != mode {
			t.Errorf("loaded %q as %q", mode, c.Agent.ExecutionMode)
		}
	}
}

// A file that says nothing takes the default, and the default has to be one of
// the accepted values or every load fails.
func TestAFileThatSaysNothingTakesAValidDefault(t *testing.T) {
	p := filepath.Join(t.TempDir(), "kaiju.json")
	if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("an empty config did not load: %v", err)
	}
	if c.Agent.ExecutionMode != "interactive" {
		t.Errorf("default execution mode is %q, want interactive", c.Agent.ExecutionMode)
	}
}
