package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

/*
 * WebSearch searches the web.
 * desc: Two providers. sonar asks Perplexity Sonar through OpenRouter and returns
 *       the sources it cites; it is the default whenever an OpenRouter key is
 *       configured. ddg scrapes DuckDuckGo's html page and is what runs without
 *       a key, and what sonar falls back to when a call fails.
 *
 *       Startpage was retired on 2026-11-12. It, and DuckDuckGo after a few
 *       queries, answer a server with a bot check, which the scraper reads as no
 *       results: runs found nothing for most queries (half the searches that day
 *       came back empty). Sonar answered the same queries with the right sources.
 */
type WebSearch struct {
	client   *http.Client
	mu       sync.Mutex
	lastAt   time.Time
	provider string        // "sonar" or "ddg"
	apiKey   string        // OpenRouter key, for sonar
	endpoint string        // OpenRouter base URL, for sonar
	delay    time.Duration // min delay between requests
}

/*
 * SearchConfig holds configuration for the web search tool.
 * desc: Passed from kaiju.json tools.web section.
 */
type SearchConfig struct {
	// Provider is "sonar" or "ddg". Empty, or a retired value ("startpage",
	// "startpage+ddg"), means sonar when APIKey is set and ddg when it is not;
	// "sonar" without a key is ddg, since there is nothing to call.
	Provider string
	APIKey   string  // the OpenRouter key sonar is called with
	Endpoint string  // the OpenRouter base URL; https://openrouter.ai/api/v1 when empty
	DelaySec float64 // min seconds between search requests (default 1.5)

	// HTTPClient replaces the default client. Nil means the default, so this
	// is invisible to every caller that does not need it.
	//
	// A tool whose whole job is an outbound request cannot be exercised without
	// one: the parsing, the provider fallback and the shape of what comes back
	// are otherwise only reachable by searching the real web from a test. That
	// is the same reason an application embedding this needs it — to prove its
	// own chain from a search result to whatever reads one, offline.
	HTTPClient *http.Client
}

/*
 * NewWebSearch creates a new WebSearch tool instance with default settings.
 * desc: No key, so DuckDuckGo, with the default delay.
 * return: pointer to a new WebSearch
 */
func NewWebSearch() *WebSearch {
	return NewWebSearchWithConfig(SearchConfig{})
}

/*
 * NewWebSearchWithConfig creates a WebSearch tool with explicit configuration.
 * desc: Configures provider and rate limit delay from config.
 * param: cfg - search configuration
 * return: pointer to a new WebSearch
 */
// One search instance per configuration, because the rate limiter that keeps this from
// tripping a search engine's anti-bot protection lives on the instance.
//
// web_search and web_research both search. They each built their own, so each waited
// out its own delay and the provider saw up to twice the configured rate — from the one
// mechanism whose whole purpose is not to.
var (
	sharedSearchMu sync.Mutex
	sharedSearches = map[SearchConfig]*WebSearch{}
)

/*
 * sharedSearch returns the one search instance for a configuration, making it on first
 * use.
 *
 * Keyed by the configuration because two tools configured differently are two different
 * limits by intent, while two configured the same are one limit that was accidentally
 * two. SearchConfig is comparable, so it is the key.
 *
 * param: cfg - the configuration.
 * return: the instance, shared with every other caller passing the same configuration.
 */
func sharedSearch(cfg SearchConfig) *WebSearch {
	sharedSearchMu.Lock()
	defer sharedSearchMu.Unlock()
	if s, ok := sharedSearches[cfg]; ok {
		return s
	}
	s := NewWebSearchWithConfig(cfg)
	sharedSearches[cfg] = s
	return s
}

func NewWebSearchWithConfig(cfg SearchConfig) *WebSearch {
	provider := searchProvider(cfg.Provider, cfg.APIKey)
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	if endpoint == "" {
		endpoint = defaultOpenRouterEndpoint
	}
	if provider == "ddg" && cfg.APIKey == "" {
		log.Printf("[web_search] no OpenRouter key: scraping DuckDuckGo, which blocks servers after a few queries. Set an OpenRouter key for reliable search.")
	}
	delay := cfg.DelaySec
	if delay <= 0 {
		delay = 0.2
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	log.Printf("[web_search] provider=%s delay=%.1fs", provider, delay)
	return &WebSearch{
		client:   client,
		provider: provider,
		apiKey:   cfg.APIKey,
		endpoint: endpoint,
		delay:    time.Duration(float64(time.Second) * delay),
	}
}

func (w *WebSearch) Name() string { return "web_search" }

func (w *WebSearch) Description() string {
	return "Search the web for information. Returns search results with titles, URLs, and " +
		"snippets. A snippet is not a source: it is the search engine's summary, not the page. " +
		"Read the results you intend to use with web_fetch, or use web_research to search and " +
		"read in one step."
}

func (w *WebSearch) Impact(map[string]any) int { return toolapi.ImpactObserve }

// RequiresTarget is false: a search goes from this node to the web, so there is
// no target for a step to name.
func (w *WebSearch) RequiresTarget() bool { return false }

func (w *WebSearch) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Search query"},
			"max_results": {"type": "integer", "description": "Maximum results to return (default: 5, max: 10)"},
			"recency_days": {"type": "integer", "description": "Optional: bias results to roughly the last N days (a recency filter — useful for time-sensitive research). Omit for no time limit."}
		},
		"required": ["query"],
		"additionalProperties": false
	}`)
}

func (w *WebSearch) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(`{"type":"object","description":"Search results with URLs.","properties":{"query":{"type":"string","description":"the search query executed"},"results":{"type":"array","description":"ranked search results","items":{"type":"object","properties":{"title":{"type":"string","description":"page title"},"url":{"type":"string","x-reference":"web_fetch.url","description":"page URL"},"snippet":{"type":"string","description":"brief excerpt from the page"}}}}}}`)
}

/*
 * Execute performs a web search and returns structured results.
 * desc: Rate-limits requests, then queries the configured provider(s).
 * param: ctx - context for cancellation and timeout
 * param: params - must contain "query"; optionally "max_results" (default 5, max 10)
 * return: JSON string with query and results array, or error
 */
// Execute satisfies the Tool interface for callers outside the DAG. The
// dispatcher prefers ExecuteTyped, which keeps the envelope the grounding edge
// reads to tell searched URLs from fetched ones.
func (w *WebSearch) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(w.ExecuteTyped(ctx, params))
}

func (w *WebSearch) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	query, _ := params["query"].(string)
	if query == "" {
		return toolapi.ToolMessage{}, fmt.Errorf("web_search: query is required")
	}

	maxResults := 5
	if mr, ok := toolapi.ParamNum(params, "max_results"); ok && mr > 0 {
		maxResults = int(mr)
		if maxResults > 10 {
			maxResults = 10
		}
	}

	// recency_days: an optional "recent results only" filter the planner can set
	// for time-sensitive research. Mapped to each provider's coarse date bucket.
	dateFilter := ""
	if rd, ok := toolapi.ParamNum(params, "recency_days"); ok {
		dateFilter = daysToBucket(int(rd))
	}

	// Rate limit: enforce minimum delay between search requests
	w.mu.Lock()
	if wait := time.Until(w.lastAt.Add(w.delay)); wait > 0 {
		w.mu.Unlock()
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return toolapi.ToolMessage{}, ctx.Err()
		}
		w.mu.Lock()
	}
	w.lastAt = time.Now()
	w.mu.Unlock()

	var results []searchResult
	var err error

	if w.provider == "sonar" {
		results, err = w.searchSonar(ctx, query, rdays(params))
		if err != nil {
			log.Printf("[web_search] sonar failed, falling back to DuckDuckGo: %v", err)
			results, err = w.searchDDG(ctx, query, maxResults, dateFilter)
		}
	} else {
		results, err = w.searchDDG(ctx, query, maxResults, dateFilter)
	}

	if err != nil {
		return toolapi.ToolMessage{}, fmt.Errorf("web_search: %w", err)
	}

	// Ground the results before they reach the planner: HEAD-validate each URL,
	// drop dead + duplicate ones, keep only reachable sources. A dead URL that
	// slips through becomes a fabricated citation — the model "fetches" a 404 and
	// invents content. Filtering here means the planner only ever web_fetches
	// URLs that actually resolve.
	total := len(results)
	results = w.filterReachable(ctx, results)
	if dropped := total - len(results); dropped > 0 {
		log.Printf("[web_search] %q: %d/%d results dropped (dead/duplicate), %d reachable", query, dropped, total, len(results))
	}

	// Never emit a bare `null` — an empty results field serializes to
	// "results": null and the model hallucinates URLs to fill it. Always return
	// an explicit [] plus a note so "no results" reads as "no results".
	if len(results) > maxResults {
		results = results[:maxResults]
	}
	if len(results) == 0 {
		detail := "no reachable results for this query — try a different query or report that nothing was found"
		if w.apiKey == "" {
			detail += ". This search scrapes DuckDuckGo, which refuses servers after a few queries; with an OpenRouter key configured, search goes through Perplexity Sonar instead"
		}
		return toolapi.ToolEmpty("search", detail), nil
	}
	// Content stays empty: the results are structured and the model reads them
	// from the payload, which Evidence() falls back to.
	return toolapi.ToolOK("search", "", map[string]any{"query": query, "results": results}), nil
}

// daysToBucket maps a recency_days count to the coarse date bucket the search
// DuckDuckGo understands (df): d, w, m, y. Returns ""
// (no filter) for <=0 or beyond a year — older than that isn't "recency".
func daysToBucket(days int) string {
	switch {
	case days <= 0:
		return ""
	case days <= 1:
		return "d"
	case days <= 7:
		return "w"
	case days <= 31:
		return "m"
	case days <= 366:
		return "y"
	default:
		return ""
	}
}

func (w *WebSearch) searchDDG(ctx context.Context, query string, max int, dateFilter string) ([]searchResult, error) {
	form := url.Values{"q": {query}, "b": {""}}
	if dateFilter != "" {
		form.Set("df", dateFilter) // DuckDuckGo date filter: d/w/m/y
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://html.duckduckgo.com/html/", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Kaiju/1.0)")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return nil, err
	}

	return parseDDGResults(string(body), max), nil
}

type searchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// urlCheckTimeout bounds each per-URL liveness probe so one slow host can't
// stall the whole search.
const urlCheckTimeout = 5 * time.Second

/*
 * filterReachable dedups result URLs and HEAD-validates each one concurrently,
 * returning only the reachable, unique results in their original rank order.
 * desc: This is the grounding gate between the scraper and the planner. Dead and
 *       duplicate URLs are dropped here so the planner only ever web_fetches URLs
 *       that resolve — a dead URL reaching the planner is what produced fabricated
 *       citations (a "verified" 404). Returns a non-nil slice (never null). May
 *       return fewer than requested; there is no backfill.
 * param: ctx - parent context (each probe gets its own sub-timeout).
 * param: results - raw scraped results, in rank order.
 * return: the reachable, deduped subset.
 */
func (w *WebSearch) filterReachable(ctx context.Context, results []searchResult) []searchResult {
	// Dedup by normalized URL, preserving rank order.
	seen := make(map[string]bool, len(results))
	deduped := make([]searchResult, 0, len(results))
	for _, r := range results {
		key := normalizeURL(r.URL)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, r)
	}

	// Probe every candidate concurrently — they target different hosts, so the
	// search rate limiter (which guards the search engine) doesn't apply.
	keep := make([]bool, len(deduped))
	var wg sync.WaitGroup
	for i := range deduped {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			keep[i] = w.urlReachable(ctx, deduped[i].URL)
		}(i)
	}
	wg.Wait()

	out := make([]searchResult, 0, len(deduped))
	for i, r := range deduped {
		if keep[i] {
			out = append(out, r)
		}
	}
	return out
}

/*
 * urlReachable probes a URL with HEAD and decides whether it's a usable source.
 * desc: Drops only URLs that are definitively dead — a transport failure
 *       (DNS/connection/TLS/timeout) or a 404/410. Everything else is KEPT,
 *       including the 405-keep rule: many servers reject HEAD with 405/501 but
 *       serve GET fine, so method-not-allowed is NOT a dead signal. Redirects
 *       are followed by the shared client, so a 3xx that lands on a live page
 *       reads as reachable.
 * param: ctx - parent context; a per-probe timeout is layered on top.
 * param: rawURL - the candidate URL.
 * return: true to keep, false to drop.
 */
func (w *WebSearch) urlReachable(ctx context.Context, rawURL string) bool {
	cctx, cancel := context.WithTimeout(ctx, urlCheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return false // unparseable URL — drop
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := w.client.Do(req)
	if err != nil {
		return false // DNS failure, connection refused, TLS error, timeout — dead
	}
	resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusGone: // 404, 410 — definitively dead
		return false
	default:
		// 2xx/3xx = live; 405/501 = HEAD refused but likely GET-able (405-keep);
		// 403/429/5xx = blocked or transiently down, NOT proof the page is gone,
		// so keep rather than false-drop a real source.
		return true
	}
}

// normalizeURL builds a dedup key from a URL: lowercased host + path (trailing
// slash trimmed) + query. Scheme and fragment are ignored so http/https and
// #anchor variants of the same page collapse to one.
func normalizeURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	key := strings.ToLower(u.Host) + strings.TrimRight(u.Path, "/")
	if u.RawQuery != "" {
		key += "?" + u.RawQuery
	}
	return key
}

func parseDDGResults(html string, max int) []searchResult {
	var results []searchResult
	remaining := html
	for len(results) < max {
		linkIdx := strings.Index(remaining, `class="result__a"`)
		if linkIdx == -1 {
			break
		}
		remaining = remaining[linkIdx:]
		hrefStart := strings.Index(remaining, `href="`)
		if hrefStart == -1 {
			break
		}
		remaining = remaining[hrefStart+6:]
		hrefEnd := strings.Index(remaining, `"`)
		if hrefEnd == -1 {
			break
		}
		rawURL := remaining[:hrefEnd]
		remaining = remaining[hrefEnd:]
		resultURL := resolveDDGURL(rawURL)
		if resultURL == "" {
			continue
		}
		titleStart := strings.Index(remaining, ">")
		if titleStart == -1 {
			break
		}
		remaining = remaining[titleStart+1:]
		titleEnd := strings.Index(remaining, "</a>")
		if titleEnd == -1 {
			break
		}
		title := stripTags(remaining[:titleEnd])
		remaining = remaining[titleEnd:]
		snippet := ""
		snippetIdx := strings.Index(remaining, `class="result__snippet"`)
		if snippetIdx != -1 && snippetIdx < 2000 {
			snipHTML := remaining[snippetIdx:]
			snipStart := strings.Index(snipHTML, ">")
			if snipStart != -1 {
				snipHTML = snipHTML[snipStart+1:]
				snipEnd := strings.Index(snipHTML, "</")
				if snipEnd != -1 {
					snippet = stripTags(snipHTML[:snipEnd])
				}
			}
		}
		if title != "" {
			results = append(results, searchResult{
				Title:   strings.TrimSpace(title),
				URL:     resultURL,
				Snippet: strings.TrimSpace(snippet),
			})
		}
	}
	return results
}

func resolveDDGURL(raw string) string {
	if strings.Contains(raw, "uddg=") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return raw
		}
		uddg := parsed.Query().Get("uddg")
		if uddg != "" {
			return uddg
		}
	}
	if strings.HasPrefix(raw, "http") {
		return raw
	}
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	return ""
}

func stripTags(s string) string {
	var out strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			out.WriteRune(r)
		}
	}
	return strings.ReplaceAll(out.String(), "&amp;", "&")
}

var _ toolapi.Tool = (*WebSearch)(nil)
var _ toolapi.Outputter = (*WebSearch)(nil)
