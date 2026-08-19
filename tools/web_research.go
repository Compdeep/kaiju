package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/toolapi"
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
		// Shared with the registered web_search rather than a second instance. The
		// limiter that keeps this from tripping a search engine's anti-bot
		// protection is per-instance, so two instances meant two limiters and up to
		// twice the configured rate at the provider.
		search: sharedSearch(cfg),
		fetch:  NewWebFetchWithLLM(executor),
	}
}

func (w *WebResearch) Name() string { return "web_research" }

func (w *WebResearch) Description() string {
	return "Search the web AND read the top results in ONE step. Runs a search, then fetches and extracts the actual text of the top result pages and returns their content. Prefer this over web_search+web_fetch for any research: every source is grounded (the URLs come from the search and are read for you), so you never invent a URL or stop at snippets. Params: query (required); optional max_sources (top results to read, default 4, max 6), recency_days, focus (the facts to extract)."
}

func (w *WebResearch) Impact(map[string]any) int { return toolapi.ImpactObserve }

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
	return toolapi.EnvelopeSchema(`{"type":"object","description":"The query, and where each answer came from. The extracted text is in content, trimmed so every source read is represented; this lists the sources themselves.","properties":{"query":{"type":"string"},"sources":{"type":"array","description":"each source read","items":{"type":"object","properties":{"url":{"type":"string"},"title":{"type":"string"},"status":{"type":"string"},"chars":{"type":"integer","description":"characters of text this source yielded, before trimming"},"trimmed":{"type":"boolean","description":"true when this source's text was cut to its share of the evidence budget"},"note":{"type":"string","description":"why a source was not read"}}}}}}`)
}

// researchSource is one page this tool read.
//
// The extracted text is not here. It was, and the readable half carried the same
// text again with each source cut to 4000 characters — so one call put every page
// in the result twice, once whole and once trimmed. Measured on one query: 11,479
// characters of text beside 20,862 of payload. The evidence cap then took the head
// and tail of that pair, so what a model finally read was the start of the text and
// the end of the JSON.
//
// The text lives in the result's content, trimmed per source so every page is
// represented. What stays here is what a later step would name: where it came from,
// whether it was read, and how much it yielded.
type researchSource struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Status  string `json:"status"` // the fetch's return code, e.g. "HTTP 200 OK" / "HTTP 404 Not Found"
	Chars   int    `json:"chars"`  // extracted text, before trimming
	Trimmed bool   `json:"trimmed,omitempty"`
	Note    string `json:"note,omitempty"`

	// content is the extracted text, kept for assembling the readable half below
	// and deliberately not serialised — see the note above.
	content string
}

// Execute satisfies the Tool interface for callers outside the DAG.
func (w *WebResearch) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(w.ExecuteTyped(ctx, params))
}

func (w *WebResearch) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	query, _ := params["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return toolapi.ToolMessage{}, fmt.Errorf("web_research: query is required")
	}
	maxSources := 4
	if v, ok := toolapi.ParamNum(params, "max_sources"); ok && int(v) > 0 {
		maxSources = int(v)
	}
	if maxSources > 6 {
		maxSources = 6
	}
	focus, _ := params["focus"].(string)

	// 1) Search — ask for a few more URLs than we'll read, as backups.
	searchParams := map[string]any{"query": query, "max_results": float64(maxSources + 3)}
	if rd, ok := toolapi.ParamNum(params, "recency_days"); ok {
		searchParams["recency_days"] = rd
	}
	sOut, err := w.search.Execute(ctx, searchParams)
	if err != nil {
		return toolapi.ToolMessage{}, fmt.Errorf("web_research: search: %w", err)
	}
	sMsg, ok := toolapi.ParseToolMessage(sOut)
	if !ok || sMsg.Status != toolapi.StatusOK {
		return toolapi.ToolEmpty("research", "the search returned no reachable results for this query — try a different, broader query"), nil
	}
	var sd struct {
		Results []searchResult `json:"results"`
	}
	if json.Unmarshal(sMsg.Data, &sd) != nil || len(sd.Results) == 0 {
		return toolapi.ToolEmpty("research", "the search returned no results — try a different query"), nil
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
			return toolapi.ToolEmpty("research", "every result was an excluded domain — broaden the query or relax exclude_domains"), nil
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
			if fMsg, ok := toolapi.ParseToolMessage(fOut); ok {
				var fd struct {
					Status string `json:"status"`
				}
				_ = json.Unmarshal(fMsg.Data, &fd)
				src.Status = fd.Status
				if fMsg.Status == toolapi.StatusOK {
					src.content = fMsg.Content
					src.Chars = len(fMsg.Content)
				} else {
					src.Note = fMsg.Detail
					if src.Status == "" {
						src.Status = string(fMsg.Status)
					}
				}
			} else {
				src.content = fOut
				src.Chars = len(fOut)
			}
			sources[i] = src
		}(i)
	}
	wg.Wait()

	// 3) Assemble the readable evidence + count how many actually returned content.
	//
	// The budget is shared between the sources rather than given to each. Each used
	// to keep 4000 characters, so four sources wrote 16,000 into a result the
	// evidence cap trims to 8,000 — and that trim is a head and tail of the whole
	// thing, which keeps the first two sources and the end of the last. A share each
	// means every page this tool went and fetched is represented in what the model
	// reads.
	perSource := toolapi.EvidenceBudget
	if n > 0 {
		perSource = toolapi.EvidenceBudget / n
	}
	if perSource < 1000 {
		perSource = 1000 // below this a page says too little to be worth having fetched
	}

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
		if strings.TrimSpace(s.content) != "" {
			read++
			c := s.content
			if len(c) > perSource {
				c = c[:perSource] + fmt.Sprintf("\n…(trimmed to this source's share of %d characters)", perSource)
				sources[i].Trimmed = true
			}
			b.WriteString(fmt.Sprintf("### Source %d — %s  [%s]\n%s\n\n%s\n\n", i+1, title, code, s.URL, c))
		} else {
			b.WriteString(fmt.Sprintf("### Source %d — %s  [%s]\n%s\n(not read: %s)\n\n", i+1, title, code, s.URL, strings.TrimSpace(s.Note)))
		}
	}

	dataBytes, _ := json.Marshal(map[string]any{"query": query, "sources": sources})
	if read == 0 {
		return toolapi.ToolMessage{
			Type:   "research",
			Status: toolapi.StatusEmpty,
			Detail: fmt.Sprintf("found %d URLs but none could be read (all blocked, 404, or empty) — try a different query", n),
			Data:   dataBytes,
		}, nil
	}
	return toolapi.ToolMessage{
		Type:    "research",
		Status:  toolapi.StatusOK,
		Content: strings.TrimRight(b.String(), "\n"),
		Data:    dataBytes,
	}, nil
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
	_ toolapi.Tool      = (*WebResearch)(nil)
	_ toolapi.Outputter = (*WebResearch)(nil)
)
