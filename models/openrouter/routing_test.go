package openrouter

import "testing"

// What the shipped lists contain, named here so that changing either is a
// deliberate act rather than a surprise. The lists reach every OpenRouter call
// in every deployment, and an entry arriving unannounced changes all of them.
//
// darkbloom is blocked on measurement, not suspicion: thirty live calls to
// qwen3.6-35b-a3b on the real planner prompt, and every reply that ignored the
// JSON schema and came back wrapped in a markdown fence was served by that host
// — five of five with reasoning on, none of five with it off, and no other host
// fenced once. Blocked by base slug rather than darkbloom/fp4, so the exclusion
// covers its other quantizations; verified live, six calls of six routed
// elsewhere where two of six had reached it unfiltered.
//
// The whitelist stays empty on purpose. Naming anything there removes fallback,
// so a listed host being down becomes a failed call rather than a slower answer
// from someone else — a blacklist gets the same exclusion and keeps the rest.
func TestTheShippedListsAreWhatWeIntend(t *testing.T) {
	blocked := Blocked()
	if len(blocked) != 1 || blocked[0] != "darkbloom" {
		t.Errorf("blacklist.json now blocks %v, want exactly [darkbloom] — intended?", blocked)
	}
	if got := Allowed(); len(got) != 0 {
		t.Errorf("whitelist.json now allows only %v, refusing every other host — intended?", got)
	}
}

// A malformed file blocks nothing and allows everything, rather than reading as
// "allow none" and refusing every provider.
func TestABrokenFileYieldsNothing(t *testing.T) {
	if got := load("whitelist.json", []byte(`{"providers":`)); got != nil {
		t.Errorf("a truncated file parsed to %v", got)
	}
	if got := load("blacklist.json", []byte(`not json`)); got != nil {
		t.Errorf("a non-JSON file parsed to %v", got)
	}
}

func TestBlankEntriesAreDropped(t *testing.T) {
	got := load("blacklist.json", []byte(`{"providers":["alibaba","","siliconflow/fp8"]}`))
	if len(got) != 2 || got[0] != "alibaba" || got[1] != "siliconflow/fp8" {
		t.Errorf("got %v, want the two real slugs", got)
	}
}

// A list of nothing but blanks is an empty list, not a list of one empty slug —
// which OpenRouter would read as a provider named "".
func TestAListOfBlanksIsEmpty(t *testing.T) {
	if got := load("whitelist.json", []byte(`{"providers":["",""]}`)); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

// The caller stamps these onto a request. Handing out the parsed slice would let
// it be scribbled on for every later call.
func TestCallersGetACopy(t *testing.T) {
	blocked = []string{"alibaba"}
	defer func() { blocked = nil }()

	got := Blocked()
	got[0] = "scribbled"
	if Blocked()[0] != "alibaba" {
		t.Error("a caller wrote through to the parsed list")
	}
}
