package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Search through the Brave Search API, the default when there is a Brave key:
// Startpage and DuckDuckGo answer a server with bot checks, so scraping them
// found nothing for most queries.

// fakeWeb answers every host from one test server: Brave's web search,
// DuckDuckGo's html search, and HEAD probes of result URLs.
type fakeWeb struct {
	srv        *httptest.Server
	mu         sync.Mutex
	braveQuery url.Values
	braveToken string
	braveCode  int
	ddgCalls   int
	fcBody     map[string]any
	fcCode     int
	fcCalls    int
}

func newFakeWeb(t *testing.T) *fakeWeb {
	f := &fakeWeb{braveCode: http.StatusOK, fcCode: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/res/v1/web/search":
			f.braveQuery = r.URL.Query()
			f.braveToken = r.Header.Get("X-Subscription-Token")
			if f.braveCode != http.StatusOK {
				w.WriteHeader(f.braveCode)
				return
			}
			hit := func(u, title, desc string) map[string]any {
				return map[string]any{"title": title, "url": u, "description": desc}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"web": map[string]any{"results": []any{
				hit("https://ssd-api.jpl.nasa.gov/doc/horizons.html", "Horizons API", "The <strong>Horizons</strong> API returns ephemerides &amp; vectors."),
				hit("https://ssd-api.jpl.nasa.gov/doc/horizons.html", "Horizons API", "duplicate"),
				hit("https://ssd.jpl.nasa.gov/horizons/", "Horizons System", ""),
				hit("https://ssd.jpl.nasa.gov/planets/orbits.html", "Planetary orbits", ""),
			}}})
		case r.URL.Path == "/v2/search":
			f.fcCalls++
			_ = json.NewDecoder(r.Body).Decode(&f.fcBody)
			if f.fcCode != http.StatusOK {
				w.WriteHeader(f.fcCode)
				return
			}
			_, _ = io.WriteString(w, `{"success":true,"creditsUsed":2,"data":{"web":[`+
				`{"url":"https://ssd-api.jpl.nasa.gov/doc/horizons.html","title":"Horizons API","description":"Horizons API &amp; parameters","position":1},`+
				`{"url":"https://ssd.jpl.nasa.gov/horizons/manual.html","title":"Horizons Manual","description":"","position":2}]}}`)
		case r.URL.Path == "/html/":
			f.ddgCalls++
			_, _ = io.WriteString(w, `<a class="result__a" href="https://ddg.example.com/page">DDG Result</a>`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// client sends every request, whatever its host, to the fake server.
func (f *fakeWeb) client() *http.Client {
	target, _ := url.Parse(f.srv.URL)
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func braveSearch(f *fakeWeb, provider, key string) *WebSearch {
	w := NewWebSearchWithConfig(SearchConfig{
		Provider: provider, APIKey: key, DelaySec: 0.001, HTTPClient: f.client(),
	})
	w.delay = time.Millisecond // the 1.1 s floor is Brave's rate, not the test's
	return w
}

func searchResults(t *testing.T, w *WebSearch, params map[string]any) []searchResult {
	t.Helper()
	msg, err := w.ExecuteTyped(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Results []searchResult `json:"results"`
	}
	b, _ := json.Marshal(msg.Data)
	_ = json.Unmarshal(b, &data)
	return data.Results
}

func TestBraveResultsAreTheSearchResults(t *testing.T) {
	f := newFakeWeb(t)
	w := braveSearch(f, "", "brave-test")
	got := searchResults(t, w, map[string]any{"query": "JPL Horizons API", "max_results": 2.0, "recency_days": 5.0})
	if len(got) != 2 || got[0].URL != "https://ssd-api.jpl.nasa.gov/doc/horizons.html" || got[1].URL != "https://ssd.jpl.nasa.gov/horizons/" {
		t.Fatalf("results = %+v, want the two Horizons pages, deduplicated and cut to max_results", got)
	}
	if want := "The Horizons API returns ephemerides & vectors."; got[0].Snippet != want {
		t.Errorf("snippet = %q, want %q (tags and entities removed)", got[0].Snippet, want)
	}
	if f.braveToken != "brave-test" {
		t.Errorf("X-Subscription-Token = %q", f.braveToken)
	}
	if f.braveQuery.Get("q") != "JPL Horizons API" || f.braveQuery.Get("count") != "2" {
		t.Errorf("query = %v", f.braveQuery)
	}
	// recency_days is a preference, never Brave's freshness filter.
	if f.braveQuery.Has("freshness") {
		t.Errorf("freshness = %q, want none: recency is a preference", f.braveQuery.Get("freshness"))
	}
	if f.ddgCalls != 0 {
		t.Errorf("DuckDuckGo was called %d times on a Brave success", f.ddgCalls)
	}
}

func TestBraveFailureFallsBackToFirecrawlThenDuckDuckGo(t *testing.T) {
	f := newFakeWeb(t)
	f.braveCode = http.StatusTooManyRequests
	w := braveSearch(f, "brave", "brave-test")
	got := searchResults(t, w, map[string]any{"query": "anything"})
	if len(got) != 2 || got[0].URL != "https://ssd-api.jpl.nasa.gov/doc/horizons.html" {
		t.Fatalf("results = %+v, want the Firecrawl results", got)
	}
	f.fcCode = http.StatusTooManyRequests
	got = searchResults(t, w, map[string]any{"query": "anything"})
	if len(got) != 1 || got[0].URL != "https://ddg.example.com/page" {
		t.Fatalf("results = %+v, want the DuckDuckGo result", got)
	}
}

// Without a Brave key, search goes through Firecrawl's keyless tier.
func TestKeylessSearchGoesThroughFirecrawl(t *testing.T) {
	t.Setenv(braveKeyEnv, "")
	f := newFakeWeb(t)
	w := braveSearch(f, "", "")
	got := searchResults(t, w, map[string]any{"query": "JPL Horizons API", "max_results": 3.0, "recency_days": 5.0})
	if len(got) != 2 || got[0].URL != "https://ssd-api.jpl.nasa.gov/doc/horizons.html" || got[1].URL != "https://ssd.jpl.nasa.gov/horizons/manual.html" {
		t.Fatalf("results = %+v, want the two Firecrawl results", got)
	}
	if got[0].Snippet != "Horizons API & parameters" {
		t.Errorf("snippet = %q", got[0].Snippet)
	}
	if f.fcBody["query"] != "JPL Horizons API" || f.fcBody["limit"] != 3.0 {
		t.Errorf("request body = %v", f.fcBody)
	}
	if _, ok := f.fcBody["tbs"]; ok {
		t.Errorf("tbs sent: recency is a preference, not a filter")
	}
	if f.ddgCalls != 0 {
		t.Errorf("DuckDuckGo was called %d times on a Firecrawl success", f.ddgCalls)
	}
}

func TestSearchProviderFollowsTheKey(t *testing.T) {
	t.Setenv(braveKeyEnv, "")
	for _, c := range []struct{ provider, key, want string }{
		{"", "k", "brave"},
		{"", "", "firecrawl"},
		{"sonar", "k", "brave"},         // retired value
		{"startpage+ddg", "k", "brave"}, // retired value
		{"brave", "", "firecrawl"},      // no key, nothing to call
		{"ddg", "k", "ddg"},             // asked for by name
		{"firecrawl", "k", "firecrawl"}, // asked for by name
	} {
		w := NewWebSearchWithConfig(SearchConfig{Provider: c.provider, APIKey: c.key})
		if w.provider != c.want {
			t.Errorf("provider %q with key %v: got %q, want %q", c.provider, c.key != "", w.provider, c.want)
		}
	}
	t.Setenv(braveKeyEnv, "from-env")
	w := NewWebSearchWithConfig(SearchConfig{})
	if w.provider != "brave" || w.apiKey != "from-env" {
		t.Errorf("with %s set: provider %q, key read %v", braveKeyEnv, w.provider, w.apiKey == "from-env")
	}
	if w.delay < braveMinDelay {
		t.Errorf("Brave delay = %v, want at least %v", w.delay, braveMinDelay)
	}
}

func TestEmptyKeylessSearchSaysAKeyHelps(t *testing.T) {
	t.Setenv(braveKeyEnv, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<html>anomaly</html>`)
	}))
	defer srv.Close()
	target, _ := url.Parse(srv.URL)
	cl := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	w := NewWebSearchWithConfig(SearchConfig{DelaySec: 0.001, HTTPClient: cl})
	msg, err := w.ExecuteTyped(context.Background(), map[string]any{"query": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.Detail, "Brave Search API key") {
		t.Errorf("the empty result should say a Brave key gives reliable search, got %q", msg.Detail)
	}
}
