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

	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
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
func NewWebSearchWithConfig(cfg SearchConfig) *WebSearch {
	provider := cfg.Provider
	if provider == "" {
		provider = "startpage+ddg"
	}
	delay := cfg.DelaySec
	if delay <= 0 {
		delay = 0.2
	}
	log.Printf("[web_search] provider=%s delay=%.1fs", provider, delay)
	return &WebSearch{
		client:   &http.Client{Timeout: 10 * time.Second},
		provider: provider,
		delay:    time.Duration(float64(time.Second) * delay),
	}
}

func (w *WebSearch) Name() string { return "web_search" }

func (w *WebSearch) Description() string {
	return "Search the web for information. Returns search results with titles, URLs, and snippets."
}

func (w *WebSearch) Impact(map[string]any) int { return agenttools.ImpactObserve }

func (w *WebSearch) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Search query"},
			"max_results": {"type": "integer", "description": "Maximum results to return (default: 5, max: 10)"}
		},
		"required": ["query"],
		"additionalProperties": false
	}`)
}

func (w *WebSearch) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","description":"Search results with URLs. Use param_refs to pass results.N.url into web_fetch.","properties":{"query":{"type":"string","description":"the search query executed"},"results":{"type":"array","description":"ranked search results","items":{"type":"object","properties":{"title":{"type":"string","description":"page title"},"url":{"type":"string","description":"page URL — chain this into web_fetch via param_refs"},"snippet":{"type":"string","description":"brief excerpt from the page"}}}}}}`)
}

/*
 * Execute performs a web search and returns structured results.
 * desc: Rate-limits requests, then queries the configured provider(s).
 * param: ctx - context for cancellation and timeout
 * param: params - must contain "query"; optionally "max_results" (default 5, max 10)
 * return: JSON string with query and results array, or error
 */
func (w *WebSearch) Execute(ctx context.Context, params map[string]any) (string, error) {
	query, _ := params["query"].(string)
	if query == "" {
		return "", fmt.Errorf("web_search: query is required")
	}

	maxResults := 5
	if mr, ok := params["max_results"].(float64); ok && mr > 0 {
		maxResults = int(mr)
		if maxResults > 10 {
			maxResults = 10
		}
	}

	// Rate limit: enforce minimum delay between search requests
	w.mu.Lock()
	if wait := time.Until(w.lastAt.Add(w.delay)); wait > 0 {
		w.mu.Unlock()
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		w.mu.Lock()
	}
	w.lastAt = time.Now()
	w.mu.Unlock()

	var results []searchResult
	var err error

	switch w.provider {
	case "startpage":
		results, err = w.searchStartpage(ctx, query, maxResults)
	case "ddg":
		results, err = w.searchDDG(ctx, query, maxResults)
	default: // "startpage+ddg"
		results, err = w.searchStartpage(ctx, query, maxResults)
		if err != nil || len(results) == 0 {
			results, err = w.searchDDG(ctx, query, maxResults)
		}
	}

	if err != nil {
		return "", fmt.Errorf("web_search: %w", err)
	}

	b, _ := json.Marshal(map[string]any{"results": results, "query": query})
	return string(b), nil
}

func (w *WebSearch) searchStartpage(ctx context.Context, query string, max int) ([]searchResult, error) {
	form := url.Values{"query": {query}, "cat": {"web"}}
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

func (w *WebSearch) searchDDG(ctx context.Context, query string, max int) ([]searchResult, error) {
	form := url.Values{"q": {query}, "b": {""}}
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

var _ agenttools.Tool = (*WebSearch)(nil)
var _ agenttools.Outputter = (*WebSearch)(nil)
