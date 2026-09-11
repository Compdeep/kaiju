package configapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/internal/config"
)

const secretKey = "sk-or-v1-THISMUSTNEVERAPPEARINARESPONSE-0000"

func configWithSecrets() *config.Config {
	c := config.Default()
	c.LLM.APIKey = secretKey
	c.Executor.APIKey = secretKey
	c.API.JWTSecret = secretKey
	c.API.AuthToken = secretKey
	c.Providers = map[string]config.ProviderConfig{
		"openai":     {APIKey: secretKey},
		"openrouter": {APIKey: secretKey},
	}
	return c
}

func getConfig(t *testing.T, c *config.Config) (string, map[string]any) {
	t.Helper()
	api := &API{cfg: c}
	rec := httptest.NewRecorder()
	api.handleGetConfig(rec, httptest.NewRequest(http.MethodGet, "/api/v1/config", nil))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	return rec.Body.String(), out
}

// No credential leaves in full, whichever field carries it.
//
// The reasoning lane's key was masked and the providers block was not, so a
// reply that looked redacted handed over every other key intact. This checks the
// whole document rather than the fields anyone thought to mask.
func TestNoCredentialIsReturnedInFull(t *testing.T) {
	body, _ := getConfig(t, configWithSecrets())
	if strings.Contains(body, secretKey) {
		t.Error("a credential was returned in full")
	}
	// The distinctive middle of the key, in case a partial mask leaks the usable
	// part while hiding the ends.
	if strings.Contains(body, "THISMUSTNEVERAPPEAR") {
		t.Error("the body of a credential was returned")
	}
}

// The signing secret and the legacy token are removed outright — nothing needs
// to see any part of them, and a masked one still says how long it is.
func TestTheSigningSecretsAreRemovedNotMasked(t *testing.T) {
	_, out := getConfig(t, configWithSecrets())
	api, _ := out["api"].(map[string]any)
	for _, k := range []string{"jwt_secret", "auth_token"} {
		if v, _ := api[k].(string); v != "" {
			t.Errorf("api.%s came back as %q; it should be empty", k, v)
		}
	}
}

// A masked key still says which key it is, because that is what an interface
// shows and a person recognises.
func TestAMaskedKeyIsStillRecognisable(t *testing.T) {
	_, out := getConfig(t, configWithSecrets())
	llm, _ := out["llm"].(map[string]any)
	got, _ := llm["api_key"].(string)
	if !strings.HasPrefix(got, "sk-o") || !strings.HasSuffix(got, "0000") {
		t.Errorf("llm.api_key = %q; a masked key keeps its first and last four", got)
	}
	if !strings.Contains(got, "****") {
		t.Errorf("llm.api_key = %q; it does not look masked", got)
	}
}

// Masking must not write through to the config the daemon is running on. The
// providers map is shared, so a careless mask redacts the live keys.
func TestMaskingDoesNotRedactTheLiveConfig(t *testing.T) {
	c := configWithSecrets()
	getConfig(t, c)
	if c.LLM.APIKey != secretKey {
		t.Errorf("the live reasoning key became %q", c.LLM.APIKey)
	}
	for name, p := range c.Providers {
		if p.APIKey != secretKey {
			t.Errorf("the live key for provider %q became %q", name, p.APIKey)
		}
	}
}

// A short key is replaced entirely: there is no length at which showing most of
// it is better than showing none.
func TestAShortKeyIsReplacedEntirely(t *testing.T) {
	if got := maskKey("abc"); got != "****" {
		t.Errorf("maskKey(\"abc\") = %q", got)
	}
	if got := maskKey(""); got != "" {
		t.Errorf("an empty key became %q; absent must stay absent", got)
	}
}
