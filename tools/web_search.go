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
 * WebSearch searches the web using configurable search providers.
 * desc: Supports Startpage (Google proxy) and DuckDuckGo. Configurable via kaiju.json.
 *       Includes a per-instance rate limiter to avoid triggering anti-bot protection.
 */
type WebSearch struct {
	client   *http.Client
	mu       sync.Mutex
	lastAt   time.Time
	provider string        // "startpage", "ddg", "startpage+ddg"
	delay    time.Duration // min delay between requests
}

/*
 * SearchConfig holds configuration for the web search tool.
 * desc: Passed from kaiju.json tools.web section.
 */
type SearchConfig struct {
	Provider string  // "startpage" (default), "ddg", "startpage+ddg"
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
 * desc: Uses Startpage+DDG fallback with 1.5s delay.
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
	provider := cfg.Provider
	if provider == "" {
		provider = "startpage+ddg"
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
		delay:    time.Duration(float64(time.Second) * delay),
	}
}

func (w *WebSearch) Name() string { return "web_search" }

func (w *WebSearch) Description() string {
	return "Search the web for information. Returns search results with titles, URLs, and snippets."
}

func (w *WebSearch) Impact(map[string]any) int { return toolapi.ImpactObserve }

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

	switch w.provider {
	case "startpage":
		results, err = w.searchStartpage(ctx, query, maxResults, dateFilter)
	case "ddg":
		results, err = w.searchDDG(ctx, query, maxResults, dateFilter)
	default: // "startpage+ddg"
		results, err = w.searchStartpage(ctx, query, maxResults, dateFilter)
		if err != nil || len(results) == 0 {
			results, err = w.searchDDG(ctx, query, maxResults, dateFilter)
		}
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
	if len(results) == 0 {
		return toolapi.ToolEmpty("search", "no reachable results for this query — try a different query or report that nothing was found"), nil
	}
	// Content stays empty: the results are structured and the model reads them
	// from the payload, which Evidence() falls back to.
	return toolapi.ToolOK("search", "", map[string]any{"query": query, "results": results}), nil
}

// daysToBucket maps a recency_days count to the coarse date bucket the search
// providers understand (DuckDuckGo df / Startpage qadf): d, w, m, y. Returns ""
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

func (w *WebSearch) searchStartpage(ctx context.Context, query string, max int, dateFilter string) ([]searchResult, error) {
	form := url.Values{"query": {query}, "cat": {"web"}}
	if dateFilter != "" {
		form.Set("qadf", dateFilter) // Startpage query-add-date-filter (best-effort)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://www.startpage.com/sp/search", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, err
	}

	return parseStartpageResults(string(body), max), nil
}

func parseStartpageResults(html string, max int) []searchResult {
	var results []searchResult
	remaining := html

	// Startpage uses combined classes like: class="result css-o7i03b"
	// Each result block contains an <a> with class containing "result-link"
	for len(results) < max {
		// Find result container: <div class="result css-...">
		idx := indexOfClass(remaining, "result")
		if idx == -1 {
			break
		}
		remaining = remaining[idx:]

		// Find the result-link anchor within this result block (within next 2000 chars)
		block := remaining
		if len(block) > 3000 {
			block = block[:3000]
		}

		linkIdx := indexOfClass(block, "result-link")
		if linkIdx == -1 {
			remaining = remaining[20:] // skip past this result marker
			continue
		}

		linkHTML := block[linkIdx:]

		// Extract href
		hrefStart := strings.Index(linkHTML, `href="`)
		if hrefStart == -1 {
			remaining = remaining[20:]
			continue
		}
		hrefStr := linkHTML[hrefStart+6:]
		hrefEnd := strings.Index(hrefStr, `"`)
		if hrefEnd == -1 {
			remaining = remaining[20:]
			continue
		}
		resultURL := hrefStr[:hrefEnd]

		if !strings.HasPrefix(resultURL, "http") {
			remaining = remaining[20:]
			continue
		}

		// Extract title: text inside the <a> tag
		title := ""
		aStart := strings.Index(linkHTML, ">")
		aEnd := strings.Index(linkHTML, "</a>")
		if aStart != -1 && aEnd != -1 && aStart < aEnd {
			title = stripTags(linkHTML[aStart+1 : aEnd])
		}

		// Extract snippet from <p> in the block
		snippet := ""
		pIdx := strings.Index(block, "<p")
		if pIdx != -1 {
			pHTML := block[pIdx:]
			pStart := strings.Index(pHTML, ">")
			if pStart != -1 {
				pHTML = pHTML[pStart+1:]
				pEnd := strings.Index(pHTML, "</p>")
				if pEnd != -1 {
					snippet = stripTags(pHTML[:pEnd])
					if len(snippet) > 200 {
						snippet = snippet[:200]
					}
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

		// Advance past this result
		remaining = remaining[linkIdx+20:]
	}

	return results
}

/*
 * indexOfClass finds the position of an HTML element whose class attribute contains the given class name.
 * desc: Searches for class="...className..." allowing combined classes like "result-title result-link css-xxx".
 * param: html - HTML string to search
 * param: className - class name to find (substring match within class attribute value)
 * return: index of the class= attribute, or -1 if not found
 */
func indexOfClass(html, className string) int {
	search := html
	offset := 0
	for {
		idx := strings.Index(search, `class="`)
		if idx == -1 {
			return -1
		}
		attrStart := idx + 7
		attrEnd := strings.Index(search[attrStart:], `"`)
		if attrEnd == -1 {
			return -1
		}
		classVal := search[attrStart : attrStart+attrEnd]
		// Check if className appears as a whole word in the class list
		for _, cls := range strings.Fields(classVal) {
			if cls == className {
				return offset + idx
			}
		}
		// Move past this class attribute
		advance := attrStart + attrEnd + 1
		search = search[advance:]
		offset += advance
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
