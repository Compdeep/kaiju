package agent

import "testing"

// A provider's key set at run time is the client its lanes route through; an
// empty key removes it.
func TestSetProvider(t *testing.T) {
	a := &Agent{}
	a.SetProvider("bedrock", ProviderCreds{Endpoint: "https://bedrock-mantle.us-east-1.api.aws/v1", APIKey: "k"})
	if a.providerClients["bedrock"] == nil {
		t.Fatal("no bedrock client")
	}
	before := a.providerClients
	a.SetProvider("openrouter", ProviderCreds{Endpoint: "https://openrouter.ai/api/v1", APIKey: "k2"})
	if len(before) != 1 {
		t.Error("the map a lane may be reading was written to")
	}
	a.SetProvider("bedrock", ProviderCreds{})
	if _, ok := a.providerClients["bedrock"]; ok || a.providerClients["openrouter"] == nil {
		t.Errorf("clients = %v", a.providerClients)
	}
}
