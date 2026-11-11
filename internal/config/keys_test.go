package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The reasoning lane's key is the key of its provider, so it moves into the
// providers catalog, where every lane looks; a provider with its own key keeps it.
func TestTheReasoningKeyMovesToItsProvider(t *testing.T) {
	c := Default()
	c.LLM.Provider, c.LLM.Endpoint, c.LLM.APIKey = "openrouter", "https://openrouter.ai/api/v1", "or-key"
	c.Providers = map[string]ProviderConfig{"bedrock": {APIKey: "br-key"}}
	c.keysByProvider()
	if p := c.Providers["openrouter"]; p.APIKey != "or-key" || p.Endpoint != "https://openrouter.ai/api/v1" {
		t.Errorf("openrouter = %+v", p)
	}
	if c.LLM.APIKey != "" {
		t.Error("the reasoning key is still held apart from its provider")
	}
	if c.Providers["bedrock"].APIKey != "br-key" {
		t.Error("another provider's key was touched")
	}

	c.LLM.APIKey = "new"
	c.keysByProvider()
	if c.Providers["openrouter"].APIKey != "or-key" {
		t.Error("a key the provider already has was replaced")
	}
}

// Each lane is built with its own provider's key, so lanes on different
// providers each work.
func TestLanesTakeTheirProvidersKey(t *testing.T) {
	c := Default()
	c.LLM.Provider, c.LLM.Endpoint = "bedrock", ""
	c.Executor.Provider = "openrouter"
	c.Providers = map[string]ProviderConfig{
		"bedrock":    {Endpoint: "https://bedrock-mantle.us-east-1.api.aws/v1", APIKey: "br-key"},
		"openrouter": {Endpoint: "https://openrouter.ai/api/v1", APIKey: "or-key"},
	}
	if l := c.ReasoningLane(); l != (Lane{"bedrock", "https://bedrock-mantle.us-east-1.api.aws/v1", "br-key"}) {
		t.Errorf("reasoning = %+v", l)
	}
	if l := c.ExecutorLane(); l != (Lane{"openrouter", "https://openrouter.ai/api/v1", "or-key"}) {
		t.Errorf("executor = %+v", l)
	}
	// A lane's own endpoint and key, where set, win.
	c.LLM.Endpoint = "https://proxy"
	c.Executor.APIKey = "exec-key"
	if l := c.ReasoningLane(); l.Endpoint != "https://proxy" {
		t.Errorf("reasoning endpoint = %s", l.Endpoint)
	}
	if l := c.ExecutorLane(); l.APIKey != "exec-key" {
		t.Errorf("executor key = %s", l.APIKey)
	}
	// No executor provider: the reasoning lane's.
	c.Executor = ExecutorConfig{}
	if l := c.ExecutorLane(); l.Provider != "bedrock" || l.APIKey != "br-key" || l.Endpoint != "https://proxy" {
		t.Errorf("executor without its own = %+v", l)
	}
}

// A file written before the providers catalog loads with its key where the
// lanes look.
func TestLoadMovesTheReasoningKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kaiju.json")
	if err := os.WriteFile(path, []byte(`{"llm":{"provider":"openai","endpoint":"https://api.openai.com/v1","api_key":"sk-old"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if l := c.ReasoningLane(); l.APIKey != "sk-old" || l.Provider != "openai" {
		t.Errorf("reasoning = %+v", l)
	}
}
