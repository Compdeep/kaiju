package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/toolapi"
	"github.com/Compdeep/kaiju/internal/configapi"
	"github.com/Compdeep/kaiju/models"
)

/*
 * Live checks of the web tools against real pages.
 *
 * Each case names facts that are on the page, checked by hand against the
 * page itself, and asks whether what the tool hands back still carries them.
 * A tool can return status ok and a fluent summary while dropping the one
 * value a plan needed: the Horizons run of 2026-11-16 read the Horizons
 * manual, whose text says "0 and ssb refer to the solar system barycenter
 * (SSB). 10 and sun refer to the center of the Sun", and passed on a summary
 * that had neither. Only a check against known content shows that.
 *
 * Off by default: it reaches the network and a model. Run with
 *
 *	KAIJU_LIVE_WEB=1 OPENROUTER_API_KEY=... go test ./tools -run TestLiveWeb -v
 *
 * KAIJU_LIVE_READERS chooses the reading models (comma separated, default
 * openai/gpt-4.1-mini and qwen/qwen3.6-35b-a3b). KAIJU_LIVE_OUT, when set, is
 * a directory each tool output is written to, to read by hand.
 */

// liveFact is one thing the output must carry. A plain string is matched
// without regard to case; a string starting with "re:" is a regular expression.
type liveFact string

func (f liveFact) in(s string) bool {
	if strings.HasPrefix(string(f), "re:") {
		return regexp.MustCompile(string(f)[3:]).MatchString(s)
	}
	return strings.Contains(strings.ToLower(s), strings.ToLower(string(f)))
}

type liveFetchCase struct {
	name   string
	url    string
	format string
	focus  string
	facts  []liveFact
}

// The pages: a reference API page, an encyclopedia article, library docs, a
// very long standard, a registry table, a data table, release notes, a raw text feed,
// a script-built page, and the manual the Horizons run read.
var liveFetchCases = []liveFetchCase{
	{"horizons-api-doc", "https://ssd-api.jpl.nasa.gov/doc/horizons.html", "extract",
		"the query parameters, how a target body is selected with COMMAND, and an example request URL",
		[]liveFact{"COMMAND", "499", "EPHEM_TYPE", "VECTORS", "horizons.api?format=", "500@399"}},
	{"horizons-manual", "https://ssd.jpl.nasa.gov/horizons/manual.html", "extract",
		"how to select the Sun and the solar system barycenter as a target or center",
		[]liveFact{"ssb", "re:\\b10\\b", "re:\\b0\\b"}},
	{"wikipedia-barycenter", "https://en.wikipedia.org/wiki/Barycenter_(astronomy)", "extract",
		"how far the Sun–Jupiter barycenter is from the Sun's center, in km and in solar radii",
		[]liveFact{"re:742[, ]?370|742,000", "1.07"}},
	{"python-json", "https://docs.python.org/3/library/json.html", "extract",
		"the full signature of json.dumps",
		[]liveFact{"skipkeys=False", "ensure_ascii=True", "sort_keys=False"}},
	{"rfc9110-retry-after", "https://www.rfc-editor.org/rfc/rfc9110.html", "extract",
		"what the Retry-After header field means and which status codes it is sent with",
		[]liveFact{"Retry-After", "503", "re:3xx|redirect"}},
	{"iana-status-codes", "https://www.iana.org/assignments/http-status-codes/http-status-codes.xhtml", "extract",
		"the status codes 429 and 503: their descriptions and the RFC that defines each",
		[]liveFact{"429", "Too Many Requests", "503", "Service Unavailable", "re:RFC ?6585"}},
	{"fed-h15", "https://www.federalreserve.gov/releases/h15/", "extract",
		"the effective federal funds rate for each day listed",
		[]liveFact{"re:(?i)federal funds", "re:\\d\\.\\d\\d"}},
	{"go122", "https://go.dev/doc/go1.22", "extract",
		"what changed about for loops",
		[]liveFact{"re:(?i)range over integers", "re:(?i)loop variable|per-iteration|each iteration"}},
	{"celestrak-tle", "https://celestrak.org/NORAD/elements/gp.php?GROUP=stations&FORMAT=tle", "markdown", "",
		[]liveFact{"ISS (ZARYA)", "25544U"}},
	{"wikipedia-barycenter-md", "https://en.wikipedia.org/wiki/Barycenter_(astronomy)", "markdown", "",
		[]liveFact{"742,370", "1.07"}},
	{"python-json-md", "https://docs.python.org/3/library/json.html", "markdown", "",
		[]liveFact{"skipkeys=False"}},
}

type liveResearchCase struct {
	name  string
	query string
	focus string
	facts []liveFact
}

var liveResearchCases = []liveResearchCase{
	{"horizons-research", "JPL Horizons API vectors Sun solar system barycenter documentation",
		"official API endpoint and parameters for requesting vector ephemerides of the Sun relative to the Solar System Barycenter",
		[]liveFact{"horizons.api", "COMMAND", "VECTORS", "re:CENTER", "re:COMMAND='?\\d+"}},
	{"python-dumps-research", "python json.dumps signature parameters", "the full signature of json.dumps and what each parameter does",
		[]liveFact{"skipkeys", "ensure_ascii", "indent"}},
	{"gpl-date-research", "GNU GPL version 3 publication date", "the date GPL version 3 was published",
		[]liveFact{"29 June 2007"}},
}

type liveSearchCase struct {
	name  string
	query string
	// host is a domain one of the results should be on.
	host string
}

var liveSearchCases = []liveSearchCase{
	{"horizons-api", "JPL Horizons API documentation", "jpl.nasa.gov"},
	{"python-json", "python json module documentation", "docs.python.org"},
	{"rfc9110", "RFC 9110 HTTP semantics", "rfc-editor.org"},
	{"celestrak-iss", "celestrak ISS TLE", "celestrak.org"},
}

func liveReaders() []string {
	if s := os.Getenv("KAIJU_LIVE_READERS"); s != "" {
		return strings.Split(s, ",")
	}
	return []string{"openai/gpt-4.1-mini", "qwen/qwen3.6-35b-a3b"}
}

func liveClient(t *testing.T, model string) *llm.Client {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		t.Skip("OPENROUTER_API_KEY not set")
	}
	// The catalog as the server wires it (cmd/kaiju: Catalog: configapi.Facts).
	// Without it the client asks nothing of a model, so the reader's "thinking
	// off" is never sent and qwen thinks into the reply budget.
	return llm.NewClientWithProvider("openrouter", "https://openrouter.ai/api/v1", key, model).
		Limits(models.Limits).Catalog(configapi.Facts)
}

func liveSave(name, body string) {
	dir := os.Getenv("KAIJU_LIVE_OUT")
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, name+".txt"), []byte(body), 0o644)
}

// liveCheck reports which facts are missing from out.
func liveCheck(out string, facts []liveFact) (missing []string) {
	for _, f := range facts {
		if !f.in(out) {
			missing = append(missing, string(f))
		}
	}
	return missing
}

func liveGate(t *testing.T) {
	if os.Getenv("KAIJU_LIVE_WEB") == "" {
		t.Skip("live web check; set KAIJU_LIVE_WEB=1")
	}
}

func TestLiveWebFetch(t *testing.T) {
	liveGate(t)
	for _, model := range liveReaders() {
		model := model
		client := liveClient(t, model)
		short := model[strings.LastIndex(model, "/")+1:]
		var mu sync.Mutex
		var wg sync.WaitGroup
		lines := make([]string, len(liveFetchCases))
		for i, c := range liveFetchCases {
			if c.format == "markdown" && model != liveReaders()[0] {
				continue // no model reads a markdown fetch; once is enough
			}
			wg.Add(1)
			go func(i int, c liveFetchCase) {
				defer wg.Done()
				ws := t.TempDir()
				w := NewWebFetchIn(ws, client, FetchLimits{})
				params := map[string]any{"url": c.url, "format": c.format}
				if c.focus != "" {
					params["focus"] = c.focus
				}
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
				defer cancel()
				start := time.Now()
				msg, err := w.ExecuteTyped(ctx, params)
				took := time.Since(start).Round(time.Second)
				if err != nil {
					mu.Lock()
					lines[i] = fmt.Sprintf("FAIL %-22s %-14s error: %v", c.name, short, err)
					mu.Unlock()
					t.Errorf("%s/%s: %v", c.name, short, err)
					return
				}
				liveSave("fetch-"+c.name+"-"+short, msg.Content+"\n\n--- detail: "+msg.Detail+"\n--- data: "+string(msg.Data))
				missing := liveCheck(msg.Content, c.facts)
				verdict := "ok  "
				if len(missing) > 0 || msg.Status != toolapi.StatusOK {
					verdict = "FAIL"
					t.Errorf("%s/%s: status %s, missing %v", c.name, short, msg.Status, missing)
				}
				mu.Lock()
				lines[i] = fmt.Sprintf("%s %-22s %-14s %-6s %6d chars %4s missing=%v", verdict, c.name, short, msg.Status, len(msg.Content), took, missing)
				mu.Unlock()
			}(i, c)
		}
		wg.Wait()
		for _, l := range lines {
			if l != "" {
				t.Log(l)
			}
		}
	}
}

func TestLiveWebResearch(t *testing.T) {
	liveGate(t)
	for _, model := range liveReaders() {
		client := liveClient(t, model)
		short := model[strings.LastIndex(model, "/")+1:]
		for _, c := range liveResearchCases {
			ws := t.TempDir()
			cfg := SearchConfig{}
			w := &WebResearch{search: NewWebSearchWithConfig(cfg), fetch: NewWebFetchIn(ws, client, FetchLimits{})}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			msg, err := w.ExecuteTyped(ctx, map[string]any{"query": c.query, "focus": c.focus, "max_sources": 3})
			cancel()
			if err != nil {
				t.Errorf("%s/%s: %v", c.name, short, err)
				continue
			}
			liveSave("research-"+c.name+"-"+short, msg.Content+"\n\n--- detail: "+msg.Detail+"\n--- data: "+string(msg.Data))
			var d struct {
				Sources []researchSource `json:"sources"`
			}
			_ = json.Unmarshal(msg.Data, &d)
			var srcs []string
			for _, s := range d.Sources {
				srcs = append(srcs, fmt.Sprintf("%s(%d,trim=%v)", hostOf(s.URL), s.Chars, s.Trimmed))
			}
			missing := liveCheck(msg.Content, c.facts)
			verdict := "ok  "
			if len(missing) > 0 {
				verdict = "FAIL"
				t.Errorf("%s/%s: missing %v", c.name, short, missing)
			}
			t.Logf("%s %-22s %-14s %6d chars missing=%v sources=%v", verdict, c.name, short, len(msg.Content), missing, srcs)
		}
	}
}

func TestLiveWebSearch(t *testing.T) {
	liveGate(t)
	w := NewWebSearchWithConfig(SearchConfig{})
	for _, c := range liveSearchCases {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		msg, err := w.ExecuteTyped(ctx, map[string]any{"query": c.query})
		cancel()
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		var d struct {
			Results []searchResult `json:"results"`
		}
		_ = json.Unmarshal(msg.Data, &d)
		rank := -1
		var hosts []string
		for i, r := range d.Results {
			hosts = append(hosts, hostOf(r.URL))
			if rank < 0 && strings.Contains(r.URL, c.host) {
				rank = i
			}
		}
		liveSave("search-"+c.name, msg.Content+"\n\n--- data: "+string(msg.Data))
		verdict := "ok  "
		if rank < 0 {
			verdict = "FAIL"
			t.Errorf("%s: no result on %s: %v", c.name, c.host, hosts)
		}
		t.Logf("%s %-14s %d results, %s at rank %d, %d chars content: %v", verdict, c.name, len(d.Results), c.host, rank, len(msg.Content), hosts)
	}
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexByte(u, '/'); i > 0 {
		u = u[:i]
	}
	return u
}
