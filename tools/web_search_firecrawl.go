package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
)

/*
 * Search through Firecrawl's keyless search.
 *
 * Firecrawl answers a search without an account or a key, up to a daily limit
 * per IP address, and returns a search engine's ranked links with an excerpt of
 * each page. It is what runs when there is no Brave key: DuckDuckGo, the only
 * other keyless choice, refuses servers after a few queries.
 */

// firecrawlEndpoint is the Firecrawl search address.
const firecrawlEndpoint = "https://api.firecrawl.dev/v2/search"

/*
 * searchFirecrawl asks Firecrawl for the top max results on query.
 * desc: No key is sent. recency_days is not passed on: Firecrawl's date control
 *       (tbs) is a filter, and it drops the older pages that answer a query
 *       about the present. Past the daily limit Firecrawl answers 429, which is
 *       returned as an error so the caller can fall back.
 * param: ctx - the call's context.
 * param: query - the search query.
 * param: max - how many results to ask for (1 to 10).
 * return: the results, or an error for a failed call or unreadable reply.
 */
func (w *WebSearch) searchFirecrawl(ctx context.Context, query string, max int) ([]searchResult, error) {
	body, _ := json.Marshal(map[string]any{"query": query, "limit": max})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, firecrawlEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %.200s", resp.StatusCode, raw)
	}

	var reply struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Data    struct {
			Web []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"web"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("unreadable reply: %w", err)
	}
	if !reply.Success {
		return nil, fmt.Errorf("search refused: %.200s", reply.Error)
	}
	out := make([]searchResult, 0, len(reply.Data.Web))
	for _, r := range reply.Data.Web {
		if !strings.HasPrefix(r.URL, "http") {
			continue
		}
		out = append(out, searchResult{
			Title:   strings.TrimSpace(html.UnescapeString(stripTags(r.Title))),
			URL:     r.URL,
			Snippet: strings.TrimSpace(html.UnescapeString(stripTags(r.Description))),
		})
	}
	return out, nil
}
