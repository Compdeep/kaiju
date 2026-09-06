package configapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Compdeep/kaiju/agent"
)

func capabilities(t *testing.T) Capabilities {
	t.Helper()
	api := &API{}
	rec := httptest.NewRecorder()
	api.handleCapabilities(rec, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("capabilities returned %d: %s", rec.Code, rec.Body.String())
	}
	var out Capabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("capabilities did not return JSON: %v", err)
	}
	return out
}

// The listing is answerable before anything is configured. A caller asks this
// endpoint to find out how to configure the node, so needing the node to be
// configured first would defeat it.
func TestCapabilitiesAnswersOnAnUnconfiguredNode(t *testing.T) {
	got := capabilities(t)
	if len(got.Settings) == 0 {
		t.Fatal("no settings listed")
	}
}

// Every value the listing publishes is one the parser accepts. This is the whole
// point of generating it: a hand-kept listing drifts from the code that enforces
// it, and a caller believing the listing then sends a value that is refused.
func TestEveryPublishedValueIsAccepted(t *testing.T) {
	parsers := map[string]func(string) (string, bool){
		"agent.execution_mode": agent.ParseExecutionMode,
		"llm.reasoning":        agent.ParseReasoning,
	}
	for _, s := range capabilities(t).Settings {
		parse, ok := parsers[s.Key]
		if !ok {
			t.Errorf("setting %q is published with no parser to check it against; "+
				"either it is free-form and should list no values, or this test needs it", s.Key)
			continue
		}
		if len(s.Values) == 0 {
			t.Errorf("setting %q publishes no values but has a parser", s.Key)
		}
		for _, v := range s.Values {
			if _, ok := parse(v); !ok {
				t.Errorf("%s publishes %q, which its own parser refuses", s.Key, v)
			}
		}
		// And the empty string, which every one of these accepts as its default.
		if _, ok := parse(""); !ok {
			t.Errorf("%s says empty means %q, and its parser refuses empty", s.Key, s.EmptyMeans)
		}
		if s.EmptyMeans == "" {
			t.Errorf("%s accepts empty and does not say what it means", s.Key)
		}
	}
}

// And the other direction: nothing the parser accepts is left unpublished, so a
// caller reading the listing sees the whole choice. The empty value is excluded
// because it is the absence of a choice, and the listing says so separately.
func TestNothingAcceptedIsLeftUnpublished(t *testing.T) {
	published := map[string][]string{}
	for _, s := range capabilities(t).Settings {
		published[s.Key] = s.Values
	}
	for key, want := range map[string][]string{
		"agent.execution_mode": agent.ExecutionModes(),
		"llm.reasoning":        agent.ReasoningModes(),
	} {
		got := published[key]
		if len(got) != len(want) {
			t.Errorf("%s publishes %v, the engine accepts %v", key, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s publishes %v, the engine accepts %v", key, got, want)
				break
			}
		}
	}
}

// Each setting says where it takes effect. Two of these read as general and are
// not: the reasoning switch is one lane's, and the other lanes decide it
// themselves, so a caller setting it globally would be wrong about what it did.
func TestEverySettingSaysWhereItApplies(t *testing.T) {
	for _, s := range capabilities(t).Settings {
		if s.Applies == "" {
			t.Errorf("setting %q does not say where it takes effect", s.Key)
		}
	}
}

// Active plugins are a subset of compiled ones. Reporting an active plugin that
// is not in the binary would send a caller looking for a feature that cannot run.
func TestActivePluginsAreCompiledOnes(t *testing.T) {
	got := capabilities(t)
	compiled := map[string]bool{}
	for _, n := range got.PluginsCompiled {
		compiled[n] = true
	}
	for _, n := range got.PluginsActive {
		if !compiled[n] {
			t.Errorf("plugin %q is reported active and is not compiled in", n)
		}
	}
}
