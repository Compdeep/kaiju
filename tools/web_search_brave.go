package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

/*
 * Search through the Brave Search API.
 *
 * Brave returns a search engine's ranked links with an excerpt of each page, as
 * a search results page does. That is what a search is for here: the address
 * of the page or service that holds the answer, which the plan then fetches.
 *
 * Perplexity Sonar, used from 2026-11-12, was removed on 2026-11-15. It is a
 * model that writes an answer and cites the articles it read, so a question
 * about a live value ("where is the solar system barycentre now") came back as
 * explainer articles with no value in them, never the address of the service
 * that computes it, and runs replanned round after round.
 */

// braveEndpoint is the Brave web search address.
const braveEndpoint = "https://api.search.brave.com/res/v1/web/search"

// braveKeyEnv is the environment variable read when the config holds no key.
const braveKeyEnv = "BRAVE_API_KEY"

// braveMinDelay is the least time between two Brave calls: the free plan
// allows one query a second, and a refused call falls back to DuckDuckGo.
const braveMinDelay = 1100 * time.Millisecond

/*
 * searchKey is the Brave key: the configured one, or BRAVE_API_KEY.
 * param: configured - the key from the config, possibly empty.
 * return: the key, or "".
 */
func searchKey(configured string) string {
	if k := strings.TrimSpace(configured); k != "" {
		return k
	}
	return strings.TrimSpace(os.Getenv(braveKeyEnv))
}

/*
 * searchProvider resolves the configured provider against the key.
 * desc: "ddg" by name is DuckDuckGo. Anything else, including an empty value
 *       and the retired "sonar", "startpage" and "startpage+ddg", is Brave
 *       when there is a key and DuckDuckGo when there is not.
 * return: "brave" or "ddg".
 */
func searchProvider(configured, apiKey string) string {
	if strings.ToLower(strings.TrimSpace(configured)) == "ddg" || apiKey == "" {
		return "ddg"
	}
	return "brave"
}

/*
 * searchBrave asks Brave for the top max results on query.
 * desc: recency_days is not passed on. Brave's only date control (freshness)
 *       is a filter, and it drops the older pages that answer a query about the
 *       present: an ephemeris service, a reference page, a library's
 *       documentation. Excerpts arrive with <strong> marks and HTML entities,
 *       which are removed.
 * param: ctx - the call's context.
 * param: query - the search query.
 * param: max - how many results to ask for (1 to 10).
 * return: the results, or an error for a failed call or unreadable reply, so
 *         the caller can fall back.
 */
func (w *WebSearch) searchBrave(ctx context.Context, query string, max int) ([]searchResult, error) {
	q := url.Values{"q": {query}, "count": {strconv.Itoa(max)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, braveEndpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", w.apiKey)

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
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("unreadable reply: %w", err)
	}
	out := make([]searchResult, 0, len(reply.Web.Results))
	for _, r := range reply.Web.Results {
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
