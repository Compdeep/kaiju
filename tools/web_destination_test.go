package tools

import (
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// Two pages of one site are one destination, and two sites are two.
//
// The whole point of the key: a plan reading ten pages of one service must be
// spaced, and a plan reading ten services must not be. Splitting the first case
// into ten keys removes the spacing; merging the second into one key removes the
// parallelism that makes research runs bearable.
func TestWebFetchDestination_GroupsBySite(t *testing.T) {
	w := NewWebFetch()
	dest := func(u string) string {
		return toolapi.GetDestination(w, map[string]any{"url": u})
	}

	if a, b := dest("https://example.org/one"), dest("https://example.org/two?x=1"); a != b {
		t.Errorf("two pages of one site got different keys (%q, %q), so they would not be spaced", a, b)
	}
	if a, b := dest("https://example.org/x"), dest("https://other.example/x"); a == b {
		t.Errorf("two sites share the key %q, so one would be slowed down for the other's limit", a)
	}
}

// A site is no less itself in capitals, on another port, or over another scheme.
// Any of those splitting the key would send the requests together again, which
// is the thing the key exists to stop.
func TestWebFetchDestination_IgnoresWhatDoesNotChangeTheSite(t *testing.T) {
	w := NewWebFetch()
	dest := func(u string) string {
		return toolapi.GetDestination(w, map[string]any{"url": u})
	}
	want := dest("https://example.org/a")
	for _, u := range []string{
		"https://EXAMPLE.ORG/a",
		"https://example.org:8443/a",
		"http://example.org/b?q=2",
		"  https://example.org/c  ",
	} {
		if got := dest(u); got != want {
			t.Errorf("%s keyed as %q, want %q — the same site would not be spaced against itself", u, got, want)
		}
	}
}

// A parameter that is not a URL says nothing about where the call goes. Empty
// is the honest answer, and it throttles with every other unaddressed call —
// which is what this tool did before it said anything about destinations.
func TestWebFetchDestination_SaysNothingWhenItCannot(t *testing.T) {
	w := NewWebFetch()
	for _, params := range []map[string]any{
		{"url": "not a url"},
		{"url": ""},
		{"url": 7},
		{},
	} {
		if got := toolapi.GetDestination(w, params); got != "" {
			t.Errorf("params %v produced the key %q, which names a destination they do not name", params, got)
		}
	}
}

// The gap is declared, and it is the gap between two calls to ONE site. A tool
// that declares a throttle and no destination throttles as a whole, so the two
// have to arrive together.
func TestWebFetchDeclaresBothOrNeither(t *testing.T) {
	w := NewWebFetch()
	if toolapi.GetThrottle(w) <= 0 {
		t.Fatal("web_fetch declares a destination and no throttle, so the destination changes nothing")
	}
	if toolapi.GetDestination(w, map[string]any{"url": "https://example.org/a"}) == "" {
		t.Fatal("web_fetch declares a throttle and no destination, so every site waits for every other")
	}
	if got := toolapi.GetThrottle(w); got > 5*time.Second {
		t.Errorf("the gap is %s — long enough that a plan reading a handful of pages stops answering", got)
	}
}
