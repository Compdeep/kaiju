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
	"github.com/user/kaiju/internal/agent/llm"
	agenttools "github.com/user/kaiju/internal/agent/tools"
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
	return json.RawMessage(`{"type":"object","description":"Fetched page content. This tool CONSUMES URLs — it does NOT produce URLs. Do not chain from this tool's output into another web_fetch.","properties":{"status":{"type":"string","description":"HTTP status line"},"title":{"type":"string","description":"page title"},"content":{"type":"string","description":"extracted page content (text, not URLs)"}}}`)
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
			"url": {"type": "string", "description": "A real HTTP/HTTPS URL to fetch. Must start with http:// or https://. Never use placeholder values — use param_refs to inject URLs from prior steps."},
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
		return fmt.Sprintf("%s\n\n%s", status, body), nil
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

/*
 * formatRaw returns the raw response body truncated to 8KB.
 * desc: Returns the unprocessed HTML body with an HTTP status prefix.
 * param: status - HTTP status line string
 * param: body - raw response body bytes
 * return: status line followed by raw body (truncated to 8KB)
 */
func (w *WebFetch) formatRaw(status string, body []byte) (string, error) {
	s := string(body)
	if len(s) > 8192 {
		s = s[:8192] + "\n... (truncated)"
	}
	return fmt.Sprintf("%s\n\n%s", status, s), nil
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

	var result strings.Builder
	result.WriteString(status)
	result.WriteString("\n")
	if article.Title != "" {
		result.WriteString(fmt.Sprintf("Title: %s\n", article.Title))
	}
	if article.Byline != "" {
		result.WriteString(fmt.Sprintf("Author: %s\n", article.Byline))
	}
	result.WriteString("\n")
	result.WriteString(content)

	return result.String(), nil
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
	return fmt.Sprintf("%s\n\n%s", status, text), nil
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

	// Truncate for LLM context (don't send 256KB to the summarizer)
	if len(content) > 16000 {
		content = content[:16000]
	}

	// Build the summary prompt
	prompt := "Extract the key information from this web page content."
	if focus != "" {
		prompt = fmt.Sprintf("Extract the following from this web page: %s. Be specific — include exact numbers, names, prices, dates where available.", focus)
	}

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
		return fmt.Sprintf("%s\nTitle: %s\n\n%s", status, title, content), nil
	}

	if len(resp.Choices) == 0 {
		return fmt.Sprintf("%s\nTitle: %s\n\n(summary failed)", status, title), nil
	}

	summary := resp.Choices[0].Message.Content
	return fmt.Sprintf("%s\nTitle: %s\n\n%s", status, title, summary), nil
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
 * param: open - opening delimiter to match (case-insensitive)
 * param: close - closing delimiter to match (case-insensitive)
 * return: string with all content between matched delimiters removed
 */
func stripBetween(s, open, close string) string {
	lower := strings.ToLower(s)
	for {
		start := strings.Index(lower, strings.ToLower(open))
		if start == -1 {
			break
		}
		end := strings.Index(lower[start:], strings.ToLower(close))
		if end == -1 {
			s = s[:start]
			lower = lower[:start]
			break
		}
		end = start + end + len(close)
		s = s[:start] + s[end:]
		lower = lower[:start] + lower[end:]
	}
	return s
}

var _ agenttools.Tool = (*WebFetch)(nil)
