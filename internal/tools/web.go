package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	readability "github.com/go-shiori/go-readability"
	"github.com/Compdeep/kaiju/internal/agent/llm"
	agenttools "github.com/Compdeep/kaiju/internal/agent/tools"
)

/*
 * WebFetch fetches a URL and extracts content in the requested format.
 * desc: Tool supporting markdown (readability), text (plain), raw (HTML), and summary (LLM extract) modes.
 */
type WebFetch struct {
	client   *http.Client
	executor *llm.Client // for summary mode (nil = summary unavailable)
}

/*
 * NewWebFetch creates a WebFetch tool without LLM summary capability.
 * desc: Initializes WebFetch with a 30-second HTTP client timeout and no LLM executor.
 * return: pointer to a new WebFetch
 */
func NewWebFetch() *WebFetch {
	return &WebFetch{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

/*
 * NewWebFetchWithLLM creates a WebFetch tool with LLM summary capability.
 * desc: Initializes WebFetch with a 30-second HTTP client and an LLM client for summary mode.
 * param: executor - LLM client used for content summarization in summary mode
 * return: pointer to a new WebFetch with LLM support
 */
func NewWebFetchWithLLM(executor *llm.Client) *WebFetch {
	return &WebFetch{
		client:   &http.Client{Timeout: 30 * time.Second},
		executor: executor,
	}
}

/*
 * Name returns the tool identifier.
 * desc: Returns "web_fetch" as the tool name.
 * return: the string "web_fetch"
 */
func (w *WebFetch) Name() string { return "web_fetch" }

/*
 * Description returns a human-readable description of the tool.
 * desc: Explains the available fetch formats: markdown, text, raw, and summary.
 * return: description string
 */
func (w *WebFetch) Description() string {
	return "Fetch a URL and extract its content. Formats: markdown (default, extracts main article content), text (plain text), raw (full HTML), summary (LLM-extracted key information with optional focus)."
}

/*
 * Impact returns the safety impact level for this tool.
 * desc: Always returns ImpactObserve since fetching URLs is non-destructive.
 * param: _ - unused parameters
 * return: ImpactObserve (0)
 */
func (w *WebFetch) Impact(map[string]any) int { return agenttools.ImpactObserve }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure with status, title, and extracted content fields.
 * return: JSON schema as raw bytes
 */
func (w *WebFetch) OutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","description":"Fetched page content as JSON. This tool CONSUMES URLs — it does NOT produce URLs. Do not chain from this tool's output into another web_fetch. Reference the extracted text in a downstream step's params with ${step.N.content}.","properties":{"status":{"type":"string","description":"HTTP status line"},"title":{"type":"string","description":"page title"},"content":{"type":"string","description":"extracted page content (text, not URLs)"},"format":{"type":"string","description":"extraction format used: markdown, text, raw, or summary"}}}`)
}

/*
 * Parameters returns the JSON schema for the tool's input parameters.
 * desc: Defines url (required), format, focus, method, body, and headers parameters.
 * return: JSON schema as raw bytes
 */
func (w *WebFetch) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "description": "A real HTTP/HTTPS URL to fetch. Must start with http:// or https://. Never use placeholder values — wire upstream URLs in via ${step.N.results.M.url} (or similar dot-paths into the upstream JSON)."},
			"format": {"type": "string", "description": "Extract mode: markdown (default), text, raw, summary", "enum": ["markdown", "text", "raw", "summary"]},
			"focus": {"type": "string", "description": "For summary mode: what to extract (e.g. 'pricing and shipping policies', 'key competitors')"},
			"method": {"type": "string", "description": "HTTP method (default: GET)", "enum": ["GET", "POST"]},
			"body": {"type": "string", "description": "Request body (for POST)"},
			"headers": {"type": "object", "description": "Additional HTTP headers", "additionalProperties": {"type": "string"}}
		},
		"required": ["url"],
		"additionalProperties": false
	}`)
}

/*
 * Execute fetches the URL and extracts content in the requested format.
 * desc: Validates the URL, performs the HTTP request, and routes to the appropriate format handler (raw, text, summary, or markdown).
 * param: ctx - context for cancellation and timeout
 * param: params - must contain "url"; optionally "format", "focus", "method", "body", "headers"
 * return: extracted content string with HTTP status, or error on invalid URL or request failure
 */
func (w *WebFetch) Execute(ctx context.Context, params map[string]any) (string, error) {
	rawURL, _ := params["url"].(string)
	if rawURL == "" {
		return "", fmt.Errorf("web_fetch: url is required")
	}

	format, _ := params["format"].(string)
	if format == "" {
		format = "markdown"
	}

	// Validate URL
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return "", fmt.Errorf("web_fetch: invalid URL %q (must start with http:// or https://)", rawURL)
	}

	method, _ := params["method"].(string)
	if method == "" {
		method = "GET"
	}

	// Fetch the page
	var bodyReader io.Reader
	if body, ok := params["body"].(string); ok && body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
	if err != nil {
		return "", fmt.Errorf("web_fetch: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Kaiju/1.0)")
	if headers, ok := params["headers"].(map[string]any); ok {
		for k, v := range headers {
			if vs, ok := v.(string); ok {
				req.Header.Set(k, vs)
			}
		}
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_fetch: %w", err)
	}
	defer resp.Body.Close()

	// Read up to 256KB for extraction (readability needs the full page)
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return "", fmt.Errorf("web_fetch: read body: %w", err)
	}

	status := fmt.Sprintf("HTTP %d %s", resp.StatusCode, resp.Status)

	if resp.StatusCode >= 400 {
		// Error responses: return raw truncated
		body := string(bodyBytes)
		if len(body) > 2048 {
			body = body[:2048] + "..."
		}
		return marshalFetchResult(fetchResult{Status: status, Content: body, Format: format})
	}

	// Route to format handler
	switch format {
	case "raw":
		return w.formatRaw(status, bodyBytes)
	case "text":
		return w.formatText(ctx, status, rawURL, bodyBytes)
	case "summary":
		focus, _ := params["focus"].(string)
		return w.formatSummary(ctx, status, rawURL, bodyBytes, focus)
	default: // markdown
		return w.formatMarkdown(ctx, status, rawURL, bodyBytes)
	}
}

// fetchResult is the structured JSON return shape of web_fetch. Declared here
// once so all formatters share it. Matches WebFetch.OutputSchema().
type fetchResult struct {
	Status  string `json:"status"`
	Title   string `json:"title,omitempty"`
	Content string `json:"content"`
	Format  string `json:"format,omitempty"`
	// Note is set when the fetch succeeded structurally but the content
	// is unusable (JS-rendered widget, login wall, summarizer refusal,
	// etc.). When Note is set, Content is empty by convention — downstream
	// callers should treat Note as a clear "no usable data" signal rather
	// than fall back to fabricated content.
	Note string `json:"note,omitempty"`
}

func marshalFetchResult(r fetchResult) (string, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

/*
 * formatRaw returns the raw response body truncated to 8KB.
 * desc: Returns the unprocessed HTML body with an HTTP status.
 * param: status - HTTP status line string
 * param: body - raw response body bytes
 * return: JSON {status, content} with body truncated to 8KB
 */
func (w *WebFetch) formatRaw(status string, body []byte) (string, error) {
	s := string(body)
	if len(s) > 8192 {
		s = s[:8192] + "\n... (truncated)"
	}
	return marshalFetchResult(fetchResult{Status: status, Content: s, Format: "raw"})
}

/*
 * formatMarkdown uses readability to extract the main content as clean text.
 * desc: Parses the page with go-readability and returns the article text, falling back to plain text on failure.
 * param: ctx - context for cancellation
 * param: status - HTTP status line string
 * param: rawURL - the original URL for readability parsing
 * param: body - raw HTML body bytes
 * return: status line with title/author and extracted article text (truncated to 12KB)
 */
func (w *WebFetch) formatMarkdown(ctx context.Context, status, rawURL string, body []byte) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		parsed = &url.URL{}
	}

	article, err := readability.FromReader(strings.NewReader(string(body)), parsed)
	if err != nil {
		// Fallback to basic text extraction if readability fails
		return w.formatText(ctx, status, rawURL, body)
	}

	content := article.TextContent
	if content == "" {
		content = article.Content // HTML fallback
	}

	// Clean up readability output
	content = strings.TrimSpace(content)
	if len(content) > 12000 {
		content = content[:12000] + "\n... (truncated)"
	}

	return marshalFetchResult(fetchResult{
		Status:  status,
		Title:   article.Title,
		Content: content,
		Format:  "markdown",
	})
}

/*
 * formatText strips all HTML and returns plain text.
 * desc: Removes HTML tags and returns clean text content truncated to 8KB.
 * param: _ - unused context
 * param: status - HTTP status line string
 * param: rawURL - the original URL (unused)
 * param: body - raw HTML body bytes
 * return: status line followed by plain text content (truncated to 8KB)
 */
func (w *WebFetch) formatText(_ context.Context, status, rawURL string, body []byte) (string, error) {
	text := stripHTML(string(body))
	if len(text) > 8192 {
		text = text[:8192] + "\n... (truncated)"
	}
	return marshalFetchResult(fetchResult{Status: status, Content: text, Format: "text"})
}

/*
 * formatSummary extracts content with readability, then uses the executor LLM to summarize.
 * desc: Combines readability extraction with LLM summarization, falling back to markdown if no LLM is available.
 * param: ctx - context for cancellation
 * param: status - HTTP status line string
 * param: rawURL - the original URL for readability parsing
 * param: body - raw HTML body bytes
 * param: focus - optional focus topic for the LLM summary prompt
 * return: status line with title and LLM-generated summary, or fallback content on failure
 */
func (w *WebFetch) formatSummary(ctx context.Context, status, rawURL string, body []byte, focus string) (string, error) {
	if w.executor == nil {
		// No LLM available, fall back to markdown
		return w.formatMarkdown(ctx, status, rawURL, body)
	}

	// First extract with readability
	parsed, _ := url.Parse(rawURL)
	if parsed == nil {
		parsed = &url.URL{}
	}

	article, _ := readability.FromReader(strings.NewReader(string(body)), parsed)

	content := ""
	title := ""
	if article.TextContent != "" {
		content = article.TextContent
		title = article.Title
	} else {
		content = stripHTML(string(body))
	}

	// If the page yielded essentially nothing extractable (interactive
	// query widgets, JS-rendered SPAs, login walls, paywalls), bail
	// early with a clear "no content" signal. Sending such pages to the
	// summarizer used to yield LLM apologies like "I don't have direct
	// access to this tool…" which downstream callers happily treated as
	// the page's actual content. Refuse to fabricate.
	if len(strings.TrimSpace(content)) < 200 {
		return marshalFetchResult(fetchResult{
			Status:  status,
			Title:   title,
			Content: "",
			Format:  "summary",
			Note:    "no extractable content (likely JS-rendered, login-walled, or an interactive widget). Try a different URL — an API endpoint or a static documentation page.",
		})
	}

	// Truncate for LLM context (don't send 256KB to the summarizer)
	if len(content) > 16000 {
		content = content[:16000]
	}

	// Build the summary prompt. Be explicit that the summarizer must
	// extract from the supplied content only — no general-knowledge
	// fallback. If the page doesn't contain the requested info, return
	// the exact sentinel below.
	const noContentSentinel = "__NO_RELEVANT_CONTENT__"
	prompt := "Extract the key information from this web page content. Use ONLY what is present in the user message; do not draw on outside knowledge."
	if focus != "" {
		prompt = fmt.Sprintf("Extract the following from this web page: %s. Be specific — include exact numbers, names, prices, dates where available. Use ONLY what is present in the user message; do not draw on outside knowledge.", focus)
	}
	prompt += fmt.Sprintf("\n\nIf the supplied content does not contain what was asked for, reply with this single token and nothing else: %s", noContentSentinel)

	if title != "" {
		prompt += fmt.Sprintf("\n\nPage title: %s", title)
	}

	resp, err := w.executor.Complete(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: prompt},
			{Role: "user", Content: content},
		},
		Temperature: 0.2,
		MaxTokens:   1024,
	})
	if err != nil {
		// LLM failed, fall back to readability text
		if len(content) > 4096 {
			content = content[:4096] + "..."
		}
		return marshalFetchResult(fetchResult{Status: status, Title: title, Content: content, Format: "summary"})
	}

	if len(resp.Choices) == 0 {
		return marshalFetchResult(fetchResult{Status: status, Title: title, Content: "", Format: "summary", Note: "summary failed (no LLM choices)"})
	}

	summary := strings.TrimSpace(resp.Choices[0].Message.Content)

	// Detect the explicit sentinel.
	if strings.Contains(summary, noContentSentinel) {
		return marshalFetchResult(fetchResult{
			Status:  status,
			Title:   title,
			Content: "",
			Format:  "summary",
			Note:    "the fetched page did not contain the requested information",
		})
	}

	// Detect summarizer refusals / apologies that escape the sentinel
	// (older models, instruction-following lapses). These are
	// recognisable as short, first-person, hedging openers; treat them
	// as "no content" rather than letting them flow downstream as fact.
	if looksLikeSummarizerRefusal(summary) {
		return marshalFetchResult(fetchResult{
			Status:  status,
			Title:   title,
			Content: "",
			Format:  "summary",
			Note:    "summarizer could not extract the requested information from the page",
		})
	}

	return marshalFetchResult(fetchResult{Status: status, Title: title, Content: summary, Format: "summary"})
}

// looksLikeSummarizerRefusal flags LLM responses that are obviously
// model meta-commentary ("I don't have access to…", "I'm sorry but…")
// rather than extracted page content. Heuristic only — cheap, no LLM.
func looksLikeSummarizerRefusal(s string) bool {
	t := strings.ToLower(strings.TrimSpace(s))
	if len(t) > 600 {
		// Real summaries are usually long. Refusals are short.
		return false
	}
	prefixes := []string{
		"i don't have", "i do not have",
		"i can't", "i cannot",
		"i'm sorry", "i am sorry",
		"i'm unable", "i am unable",
		"sorry, ", "as an ai",
		"i don't see", "i do not see",
		"the page does not", "the content does not",
		"there is no", "there isn't",
		"no information", "no relevant",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

/*
 * stripHTML removes all HTML tags and returns clean text.
 * desc: Strips script/style/nav blocks, removes tags, decodes entities, and collapses whitespace.
 * param: html - raw HTML string to clean
 * return: plain text with tags removed and whitespace normalized
 */
func stripHTML(html string) string {
	// Remove script/style/nav blocks
	for _, tag := range []string{"script", "style", "nav", "header", "footer", "noscript"} {
		html = stripBetween(html, "<"+tag, "</"+tag+">")
	}
	html = stripBetween(html, "<!--", "-->")

	// Strip remaining tags
	var out strings.Builder
	inTag := false
	for _, r := range html {
		if r == '<' {
			inTag = true
			out.WriteRune(' ')
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

	// Decode entities
	text := out.String()
	for _, pair := range [][2]string{
		{"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"},
		{"&quot;", "\""}, {"&#x27;", "'"}, {"&nbsp;", " "},
		{"&#39;", "'"}, {"&#x2F;", "/"},
	} {
		text = strings.ReplaceAll(text, pair[0], pair[1])
	}

	// Collapse whitespace
	lines := strings.Split(text, "\n")
	var cleaned []string
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, "\n")
}

/*
 * stripBetween removes everything between open and close tags.
 * desc: Repeatedly finds and removes content between the specified open and close delimiters.
 * param: s - input string to process
 * param: open - opening delimiter to match (case-insensitive ASCII)
 * param: close - closing delimiter to match (case-insensitive ASCII)
 * return: string with all content between matched delimiters removed
 *
 * Case-insensitive search is done directly on s via indexFoldASCII so
 * indices line up with the original string. An earlier version kept a
 * parallel `lower := strings.ToLower(s)` and used its indices to slice
 * s; that panics when the input contains characters whose lowercase
 * form has a different byte length (İ → i\u0307, some Greek/Turkish
 * letters, etc.) because the two strings drift in length.
 */
func stripBetween(s, open, close string) string {
	for {
		start := indexFoldASCII(s, open)
		if start == -1 {
			break
		}
		rel := indexFoldASCII(s[start:], close)
		if rel == -1 {
			s = s[:start]
			break
		}
		end := start + rel + len(close)
		s = s[:start] + s[end:]
	}
	return s
}

// indexFoldASCII reports the byte index of the first case-insensitive
// (ASCII-only) match of needle in s, or -1 if absent. Non-ASCII bytes
// compare exactly. The HTML tags this is used on (script, style, etc.)
// are ASCII so this is sufficient and avoids the unicode case-folding
// length drift that broke stripBetween.
func indexFoldASCII(s, needle string) int {
	n := len(needle)
	if n == 0 {
		return 0
	}
	if n > len(s) {
		return -1
	}
	for i := 0; i+n <= len(s); i++ {
		match := true
		for j := 0; j < n; j++ {
			sb := s[i+j]
			nb := needle[j]
			if sb >= 'A' && sb <= 'Z' {
				sb |= 0x20
			}
			if nb >= 'A' && nb <= 'Z' {
				nb |= 0x20
			}
			if sb != nb {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

var _ agenttools.Tool = (*WebFetch)(nil)
