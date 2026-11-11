package configapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/internal/config"
	"github.com/Compdeep/kaiju/models"
)

func patchConfig(t *testing.T, c *config.Config, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	(&API{cfg: c}).handleUpdateConfig(rec, httptest.NewRequest(http.MethodPatch, "/api/v1/config", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH %s = %d: %s", body, rec.Code, rec.Body.String())
	}
}

// A provider's key is saved under that provider, with its address, and is
// masked when read back; another provider's is untouched.
func TestAProviderKeyIsSavedUnderItsProvider(t *testing.T) {
	c := config.Default()
	c.Providers = map[string]config.ProviderConfig{"openrouter": {Endpoint: "https://openrouter.ai/api/v1", APIKey: "or-key-000000"}}
	patchConfig(t, c, `{"providers":{"bedrock":{"endpoint":"https://bedrock-mantle.us-east-1.api.aws/v1","api_key":"br-key-0000000000"}}}`)
	if p := c.Providers["bedrock"]; p.APIKey != "br-key-0000000000" || p.Endpoint != "https://bedrock-mantle.us-east-1.api.aws/v1" {
		t.Errorf("bedrock = %+v", p)
	}
	if c.Providers["openrouter"].APIKey != "or-key-000000" {
		t.Error("openrouter's key was touched")
	}
	// An empty key leaves the saved one alone.
	patchConfig(t, c, `{"providers":{"bedrock":{"api_key":""}}}`)
	if c.Providers["bedrock"].APIKey != "br-key-0000000000" {
		t.Error("an empty key cleared the saved one")
	}
	if body, _ := getConfig(t, c); strings.Contains(body, "br-key-0000000000") {
		t.Error("the key was returned in full")
	}
}

// The older single key is the reasoning provider's key.
func TestTheReasoningKeyIsItsProvidersKey(t *testing.T) {
	c := config.Default()
	c.LLM.Provider = "bedrock"
	patchConfig(t, c, `{"llm":{"api_key":"br-key-0000000000"}}`)
	if c.Providers["bedrock"].APIKey != "br-key-0000000000" || c.LLM.APIKey != "" {
		t.Errorf("providers %+v, llm key %q", c.Providers, c.LLM.APIKey)
	}
}

// The model list carries each model's thinking variants.
func TestTheModelListCarriesThinkingVariants(t *testing.T) {
	c := config.Default()
	rec := httptest.NewRecorder()
	(&API{cfg: c}).handleListModels(rec, httptest.NewRequest(http.MethodGet, "/api/v1/models", nil))
	if !strings.Contains(rec.Body.String(), `"id":"qwen.qwen3-32b+high"`) {
		t.Error("qwen.qwen3-32b+high is not listed")
	}
	if len(models.WithVariants(models.All())) <= len(models.All()) {
		t.Error("no variants")
	}
}
