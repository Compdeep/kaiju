package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

/*
 * Search through Perplexity Sonar on OpenRouter.
 *
 * Sonar searches the web itself and cites what it read; the citations are the
 * results. Its written answer is not passed on: a search result is a lead to
 * fetch, not evidence, and the same holds for a model's summary of one.
 *
 * Measured 2026-11-12 on a query scraping had returned nothing for ("JPL
 * Horizons API heliocentric positions planets November 2026"): 8 sources,
 * including the Horizons API documentation, in 4.8 s for about $0.005.
 */

// sonarModel is the model searched with.
const sonarModel = "perplexity/sonar"

// defaultOpenRouterEndpoint is used when the config gives no endpoint.
const defaultOpenRouterEndpoint = "https://openrouter.ai/api/v1"

// sonarMaxTokens bounds the answer Sonar writes. Only its citations are read,
// so the answer is kept short to keep the call cheap and quick.
const sonarMaxTokens = 300

/*
 * searchProvider resolves the configured provider against the key.
 * return: "sonar" or "ddg".
 */
func searchProvider(configured, apiKey string) string {
	if strings.ToLower(strings.TrimSpace(configured)) == "ddg" || apiKey == "" {
		return "ddg"
	}
	return "sonar"
}

// rdays is the step's recency_days, or 0.
func rdays(params map[string]any) int {
	if rd, ok := toolapi.ParamNum(params, "recency_days"); ok && rd > 0 {
		return int(rd)
	}
	return 0
}

/*
 * sonarInstruction is the system message Sonar searches by.
 * desc: recency_days is a preference, said in words, not Perplexity's
 *       search_recency_filter: that filter drops every page published before
 *       the window, and the pages that answer "where is X now" (an ephemeris
 *       service, a reference page, a library's documentation) are old pages
 *       serving current data. Measured 2026-11-14 on "current solar system
 *       barycenter position": a week's filter returned five unrelated pages,
 *       none without it.
 */
func sonarInstruction(recencyDays int) string {
	s := "Find the web pages that best answer the query and cite them."
	if recencyDays > 0 {
		s += fmt.Sprintf(" Prefer pages from the last %d days where the query is about recent events or news;"+
			" a reference page or a live data source that answers the query counts whatever its age.", recencyDays)
	}
	return s
}

/*
 * searchSonar asks Sonar for sources on query.
 * desc: Results are the reply's url_citation annotations, each URL once, in the
 *       order cited. Annotations carry no excerpt, so a result's snippet is the
 *       sentences of the reply that cite it.
 * return: the results, or an error for any failed call or unreadable reply, so
 *         the caller can fall back.
 */
func (w *WebSearch) searchSonar(ctx context.Context, query string, recencyDays int) ([]searchResult, error) {
	body := map[string]any{
		"model":      sonarModel,
		"max_tokens": sonarMaxTokens,
		"messages": []map[string]string{
			{"role": "system", "content": sonarInstruction(recencyDays)},
			{"role": "user", "content": query},
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.apiKey)

	resp, err := w.sonarClient().Do(req)
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
		Choices []struct {
			Message struct {
				Content     string `json:"content"`
				Annotations []struct {
					Type        string `json:"type"`
					URLCitation struct {
						URL     string `json:"url"`
						Title   string `json:"title"`
						Content string `json:"content"`
					} `json:"url_citation"`
				} `json:"annotations"`
			} `json:"message"`
		} `json:"choices"`
		Citations []string `json:"citations"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("unreadable reply: %w", err)
	}
	if len(reply.Choices) == 0 {
		return nil, fmt.Errorf("reply has no choices")
	}

	var out []searchResult
	seen := map[string]bool{}
	add := func(u, title, snippet string) {
		if !strings.HasPrefix(u, "http") || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, searchResult{Title: title, URL: u, Snippet: snippet})
	}
	// The reply's own sentences are the snippets: a citation carries no text,
	// and the answer marks each sentence with [n] for the n-th citation. With
	// no snippet a result was a bare title and address, and an old example
	// query on an API's host read as the API itself.
	cited := citedSentences(reply.Choices[0].Message.Content)
	n := 0
	for _, a := range reply.Choices[0].Message.Annotations {
		if a.Type == "url_citation" {
			n++
			snippet := a.URLCitation.Content
			if snippet == "" {
				snippet = cited[n]
			}
			add(a.URLCitation.URL, a.URLCitation.Title, snippet)
		}
	}
	for _, u := range reply.Citations {
		add(u, "", "")
	}
	return out, nil
}

// sonarClient is the search client with room for Sonar's own search: the
// default 10 s is sized for fetching one results page.
func (w *WebSearch) sonarClient() *http.Client {
	if w.client.Timeout == 0 || w.client.Timeout >= sonarTimeout {
		return w.client
	}
	c := *w.client
	c.Timeout = sonarTimeout
	return &c
}

// sonarTimeout bounds one Sonar call, which searches and reads before replying
// (about 5 s measured).
const sonarTimeout = 45 * time.Second

// citedMarker is a run of citation marks, such as [2][17].
var citedMarker = regexp.MustCompile(`(?:\[\d+\])+`)

// citedNumber is one number in a run of citation marks.
var citedNumber = regexp.MustCompile(`\d+`)

// snippetMax bounds one result's snippet.
const snippetMax = 300

/*
 * citedSentences maps each citation number to the sentences of answer that
 * cite it.
 * desc: Sonar ends a sentence with its citation marks: "...outside the Sun's
 *       surface.[1][3]". The sentence is the text since the previous sentence
 *       end or mark. Markdown emphasis is dropped and each snippet is cut to
 *       snippetMax at a word.
 * param: answer - the reply's text
 * return: citation number (1-based) to snippet
 */
func citedSentences(answer string) map[int]string {
	out := map[int]string{}
	prev := 0
	for _, loc := range citedMarker.FindAllStringIndex(answer, -1) {
		sentence := lastSentence(answer[prev:loc[0]])
		prev = loc[1]
		if sentence == "" {
			continue
		}
		for _, m := range citedNumber.FindAllString(answer[loc[0]:loc[1]], -1) {
			num, _ := strconv.Atoi(m)
			if cur := out[num]; cur == "" {
				out[num] = sentence
			} else if !strings.Contains(cur, sentence) {
				out[num] = cur + " " + sentence
			}
		}
	}
	for k, v := range out {
		if len(v) > snippetMax {
			cut := strings.LastIndex(v[:snippetMax], " ")
			if cut <= 0 {
				cut = snippetMax
			}
			out[k] = v[:cut] + "…"
		}
	}
	return out
}

// lastSentence is the last sentence of text, without markdown emphasis or a
// list marker. A sentence ends at a stop followed by a space or at a line
// break, so a decimal point or "e.g." inside it does not end it.
func lastSentence(text string) string {
	text = strings.TrimSpace(strings.NewReplacer("**", "", "__", "", "*", "").Replace(text))
	start := strings.LastIndex(text, "\n")
	for _, stop := range []string{". ", "! ", "? "} {
		if i := strings.LastIndex(text, stop); i+1 > start {
			start = i + 1
		}
	}
	if start >= 0 {
		text = text[start:]
	}
	text = strings.TrimLeft(strings.TrimSpace(text), "-•# ")
	return strings.Join(strings.Fields(text), " ")
}
