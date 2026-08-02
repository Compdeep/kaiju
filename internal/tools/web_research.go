package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/Compdeep/kaiju/agent/llm"
	agenttools "github.com/Compdeep/kaiju/agent/tools"
)

// WebResearch searches the web AND reads the top results in ONE step: it runs a
// search, then fetches and extracts the content of the top result pages, and
// returns their text. This takes URL-selection and the fetch decision out of the
// planner's hands entirely — every URL comes from the search (never invented by
// the model) and is read by code (never skipped) — so a research step can neither
// fabricate a source nor stop at snippets. It reuses the existing web_search and
// web_fetch pipelines internally.
type WebResearch struct {
	search *WebSearch
	fetch  *WebFetch
}

// NewWebResearch builds the tool from the same config web_search/web_fetch use.
func NewWebResearch(cfg SearchConfig, executor *llm.Client) *WebResearch {
	return &WebResearch{
		search: NewWebSearchWithConfig(cfg),
		fetch:  NewWebFetchWithLLM(executor),
	}
}

func (w *WebResearch) Name() string { return "web_research" }

func (w *WebResearch) Description() string {
	return "Search the web AND read the top results in ONE step. Runs a search, then fetches and extracts the actual text of the top result pages and returns their content. Prefer this over web_search+web_fetch for any research: every source is grounded (the URLs come from the search and are read for you), so you never invent a URL or stop at snippets. Params: query (required); optional max_sources (top results to read, default 4, max 6), recency_days, focus (the facts to extract)."
}

func (w *WebResearch) Impact(map[string]any) int { return agenttools.ImpactObserve }

func (w *WebResearch) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Search query — plain keywords, not stacked search operators."},
			"max_sources": {"type": "integer", "description": "How many of the top results to fetch and read (default 4, max 6)."},
			"recency_days": {"type": "integer", "description": "Optional: bias to results from roughly the last N days."},
			"focus": {"type": "string", "description": "Optional: the specific facts/figures to extract from each page."},
			"exclude_domains": {"type": "array", "items": {"type": "string"}, "description": "Optional: domains to drop from the results (e.g. aggregators like statista.com, fortunebusinessinsights.com)."}
		},
		"required": ["query"],
		"additionalProperties": false
	}`)
}

func (w *WebResearch) OutputSchema() json.RawMessage {
	return agenttools.EnvelopeSchema(`{"type":"object","description":"Research results: the query plus the sources actually read.","properties":{"query":{"type":"string"},"sources":{"type":"array","description":"each source read","items":{"type":"object","properties":{"url":{"type":"string"},"title":{"type":"string"},"content":{"type":"string"},"note":{"type":"string"}}}}}}`)
}

type researchSource struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Status  string `json:"status"` // the fetch's return code, e.g. "HTTP 200 OK" / "HTTP 404 Not Found"
	Content string `json:"content,omitempty"`
	Note    string `json:"note,omitempty"`
}

func (w *WebResearch) Execute(ctx context.Context, params map[string]any) (string, error) {
	query, _ := params["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("web_research: query is required")
	}
	maxSources := 4
	if v, ok := params["max_sources"].(float64); ok && int(v) > 0 {
		maxSources = int(v)
	}
	if maxSources > 6 {
		maxSources = 6
	}
	focus, _ := params["focus"].(string)

	// 1) Search — ask for a few more URLs than we'll read, as backups.
	searchParams := map[string]any{"query": query, "max_results": float64(maxSources + 3)}
	if rd, ok := params["recency_days"].(float64); ok {
		searchParams["recency_days"] = rd
	}
	sOut, err := w.search.Execute(ctx, searchParams)
	if err != nil {
		return "", fmt.Errorf("web_research: search: %w", err)
	}
	sMsg, ok := agenttools.ParseToolMessage(sOut)
	if !ok || sMsg.Status != agenttools.StatusOK {
		return agenttools.ToolEmpty("research", "the search returned no reachable results for this query — try a different, broader query").JSON(), nil
	}
	var sd struct {
		Results []searchResult `json:"results"`
	}
	if json.Unmarshal(sMsg.Data, &sd) != nil || len(sd.Results) == 0 {
		return agenttools.ToolEmpty("research", "the search returned no results — try a different query").JSON(), nil
	}

	// Drop excluded domains (e.g. aggregators the caller doesn't want) before we
	// pick which results to read.
	if ex := toStringSlice(params["exclude_domains"]); len(ex) > 0 {
		kept := sd.Results[:0]
		for _, r := range sd.Results {
			if !hostExcluded(r.URL, ex) {
				kept = append(kept, r)
			}
		}
		sd.Results = kept
		if len(sd.Results) == 0 {
			return agenttools.ToolEmpty("research", "every result was an excluded domain — broaden the query or relax exclude_domains").JSON(), nil
		}
	}

	// 2) Fetch the top results in parallel — different hosts, so the search rate
	// limiter (which guards the search engine) doesn't apply.
	n := maxSources
	if n > len(sd.Results) {
		n = len(sd.Results)
	}
	sources := make([]researchSource, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := sd.Results[i]
			src := researchSource{URL: r.URL, Title: r.Title}
			fParams := map[string]any{"url": r.URL, "format": "summary"}
			if focus != "" {
				fParams["focus"] = focus
			}
			fOut, ferr := w.fetch.Execute(ctx, fParams)
			if ferr != nil {
				src.Status = "no response"
				src.Note = "fetch error: " + ferr.Error()
				sources[i] = src
				return
			}
			if fMsg, ok := agenttools.ParseToolMessage(fOut); ok {
				var fd struct {
					Status string `json:"status"`
				}
				_ = json.Unmarshal(fMsg.Data, &fd)
				src.Status = fd.Status
				if fMsg.Status == agenttools.StatusOK {
					src.Content = fMsg.Content
				} else {
					src.Note = fMsg.Detail
					if src.Status == "" {
						src.Status = string(fMsg.Status)
					}
				}
			} else {
				src.Content = fOut
			}
			sources[i] = src
		}(i)
	}
	wg.Wait()

	// 3) Assemble the readable evidence + count how many actually returned content.
	var b strings.Builder
	read := 0
	for i, s := range sources {
		title := s.Title
		if title == "" {
			title = s.URL
		}
		code := s.Status
		if code == "" {
			code = "no response"
		}
		if strings.TrimSpace(s.Content) != "" {
			read++
			c := s.Content
			if len(c) > 4000 {
				c = c[:4000] + "\n…(truncated)"
			}
			b.WriteString(fmt.Sprintf("### Source %d — %s  [%s]\n%s\n\n%s\n\n", i+1, title, code, s.URL, c))
		} else {
			b.WriteString(fmt.Sprintf("### Source %d — %s  [%s]\n%s\n(not read: %s)\n\n", i+1, title, code, s.URL, strings.TrimSpace(s.Note)))
		}
	}

	dataBytes, _ := json.Marshal(map[string]any{"query": query, "sources": sources})
	if read == 0 {
		return agenttools.ToolMessage{
			Kind:   "research",
			Status: agenttools.StatusEmpty,
			Detail: fmt.Sprintf("found %d URLs but none could be read (all blocked, 404, or empty) — try a different query", n),
			Data:   dataBytes,
		}.JSON(), nil
	}
	return agenttools.ToolMessage{
		Kind:    "research",
		Status:  agenttools.StatusOK,
		Content: strings.TrimRight(b.String(), "\n"),
		Data:    dataBytes,
	}.JSON(), nil
}

// toStringSlice coerces a JSON array param (decoded as []any) into a lowercased,
// trimmed []string, dropping non-strings and blanks.
func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// hostExcluded reports whether rawURL's host contains any of the excluded domains.
func hostExcluded(rawURL string, excluded []string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	for _, d := range excluded {
		if strings.Contains(host, d) {
			return true
		}
	}
	return false
}

var (
	_ agenttools.Tool      = (*WebResearch)(nil)
	_ agenttools.Outputter = (*WebResearch)(nil)
)
