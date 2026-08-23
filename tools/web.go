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
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Compdeep/kaiju/agent/llm"
	"github.com/Compdeep/kaiju/agent/toolapi"
	readability "github.com/go-shiori/go-readability"
)

/*
 * WebFetch fetches a URL, keeps the page, and returns the part that was asked
 * for.
 * desc: The body is written to the workspace on every fetch and the result says
 *       where, so a caller that asked for the wrong shape has not lost the
 *       document — a later step can read, search or parse the file with none of
 *       the limits that apply to a result travelling into a prompt.
 *
 *       Inline, it returns what was asked for: the page as text, or the parts
 *       of it matching a focus. How much of the page the extractor is shown, and
 *       how long a reply it may write, come from the model that will read it.
 */
type WebFetch struct {
	client    *http.Client
	executor  *llm.Client // for extract mode (nil = extract unavailable)
	workspace string      // where a fetched body is kept ("" = nowhere)
	limits    FetchLimits
}

/*
 * NewWebFetch creates a WebFetch that keeps no page and extracts nothing.
 * desc: No workspace, so nothing is written and no path comes back; no model,
 *       so a focus cannot be answered. It reads a page and returns it.
 * return: pointer to a new WebFetch
 */
func NewWebFetch() *WebFetch {
	return &WebFetch{
		client: &http.Client{Timeout: 30 * time.Second},
		limits: FetchLimits{}.resolve(),
	}
}

/*
 * NewWebFetchWithLLM creates a WebFetch that can extract from what it fetched.
 * desc: Kept for callers that have no workspace to keep pages in. Prefer
 *       NewWebFetchIn, which does — the page surviving the fetch is what makes
 *       a wrong guess about the inline shape recoverable.
 * param: executor - the model that answers a focus
 * return: pointer to a new WebFetch
 */
func NewWebFetchWithLLM(executor *llm.Client) *WebFetch {
	return NewWebFetchIn("", executor, FetchLimits{})
}

/*
 * NewWebFetchIn creates a WebFetch that keeps every page it reads.
 * desc: workspace is where bodies are written. Empty means none is, and the
 *       result carries no path — everything else works as before, so a caller
 *       without a sandbox is not broken, only less able to recover.
 * param: workspace - the directory bodies are written under, or "".
 * param: executor - the model that answers a focus, or nil.
 * param: limits - what one fetch may spend; the zero value is this package's.
 * return: pointer to a new WebFetch
 */
func NewWebFetchIn(workspace string, executor *llm.Client, limits FetchLimits) *WebFetch {
	return &WebFetch{
		client:    &http.Client{Timeout: 30 * time.Second},
		executor:  executor,
		workspace: workspace,
		limits:    limits.resolve(),
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
func (w *WebFetch) Impact(map[string]any) int { return toolapi.ImpactObserve }

/*
 * OutputSchema returns the JSON schema for the tool's output.
 * desc: Defines the output structure with status, title, and extracted content fields.
 * return: JSON schema as raw bytes
 */
// Excerpts declares that content is cut to what fits a prompt while path names
// the whole page. A step wired to content when the page did not come back whole
// is refused, because it would work on part of the document and report its
// answer as covering all of it.
func (w *WebFetch) Excerpts() []toolapi.Excerpt {
	return []toolapi.Excerpt{{
		Field: "content",
		Whole: "full_content_path",
		Size:  "bytes",
		Use:   "read full_content_path in this step: content is cut down to what fits a prompt, and that file is the whole page",
	}}
}

func (w *WebFetch) OutputSchema() json.RawMessage {
	return toolapi.EnvelopeSchema(`{"type":"object","description":"Fetched page content as JSON. This tool CONSUMES URLs — it does NOT produce URLs. Do not chain from this tool's output into another web_fetch. Reference the extracted text in a downstream step's params with ${step.N.content}.","properties":{"status":{"type":"string","description":"HTTP status line"},"title":{"type":"string","description":"page title"},"content":{"type":"string","description":"extracted page content (text, not URLs)"},"format":{"type":"string","description":"what was returned inline: markdown, text, or extract"},"full_content_path":{"type":"string","description":"the file holding the whole page, relative to the workspace. Present on every fetch that has somewhere to write. content is cut down to what fits a prompt; this file is not. A step that has to work over the whole document — count, search, total — reads this file"},"bytes":{"type":"integer","description":"the size of the file at full_content_path, so it can be compared with how much came back in content"},"body_truncated":{"type":"boolean","description":"the page was larger than this deployment keeps, so even full_content_path holds the beginning of it and not all of it"},"url":{"type":"string","description":"the URL this call was given, echoed back so a later step can say which page a result came from. It is not a link found on the page, and fetching it again returns this same result — see the warning above about not chaining from this tool into another web_fetch"}}}`)
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
			"format": {"type": "string", "description": "What to return inline. markdown (default) — the page as clean text, best for reading a reference document you are going to work from. text — the same, stripped of all markup. extract — only the parts matching the focus, quoted word for word, read across the WHOLE page; use this when you need exact names, parameters or figures, because it does not paraphrase. The full page is always written to disk and its path returned, whichever you pick.", "enum": ["markdown", "text", "extract", "summary"]},
			"focus": {"type": "string", "description": "For summary mode: what to extract (e.g. 'pricing and shipping policies', 'key competitors')"},
			"method": {"type": "string", "description": "HTTP method (default: GET)", "enum": ["GET", "POST"]},
			"body": {"type": "string", "description": "Request body (for POST)"},
			"headers": {"type": "object", "description": "Additional HTTP headers (override the browser defaults). Rarely needed — a full browser header set is sent automatically.", "additionalProperties": {"type": "string"}},
			"referer": {"type": "string", "description": "The page this URL was found on — set it to where you got the link (the search-results page, or the site's own homepage like https://example.com/). Many sites return 403 for requests with a blank referer; supplying a plausible one often gets through. Optional but recommended when fetching a ${step.N.results.M.url} from a search."}
		},
		"required": ["url"],
		"additionalProperties": false
	}`)
}

/*
 * setBrowserHeaders stamps a coherent desktop-Chrome (Linux) request fingerprint.
 * desc: A large share of 403s are naive bot-blocks keyed on the User-Agent or a
 *       blank referer. We send a full, internally-consistent header set — UA,
 *       sec-ch-ua client hints, and Sec-Fetch-* — because anti-bot filters flag
 *       a lone browser UA with no matching client hints as MORE bot-like, not
 *       less. Deliberately no Accept-Encoding: leaving it unset lets Go's
 *       transport add gzip and transparently decompress; setting br/gzip here
 *       would hand the format handlers undecoded bytes. This does NOT defeat
 *       TLS/HTTP-2 fingerprinting (Cloudflare/Akamai) or JS challenges.
 * param: req - the request to stamp (headers set in place).
 * param: referer - the page the URL was found on; "" leaves Referer unset.
 */
func setBrowserHeaders(req *http.Request, referer string) {
	h := req.Header
	h.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	h.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("sec-ch-ua", `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`)
	h.Set("sec-ch-ua-mobile", "?0")
	h.Set("sec-ch-ua-platform", `"Linux"`)
	h.Set("Sec-Fetch-Dest", "document")
	h.Set("Sec-Fetch-Mode", "navigate")
	h.Set("Sec-Fetch-User", "?1")
	h.Set("Upgrade-Insecure-Requests", "1")
	if referer != "" {
		h.Set("Referer", referer)
		// A link followed from another origin is a cross-site navigation.
		h.Set("Sec-Fetch-Site", "cross-site")
	} else {
		h.Set("Sec-Fetch-Site", "none")
	}
}

/*
 * Execute fetches the URL and extracts content in the requested format.
 * desc: Validates the URL, performs the HTTP request, and routes to the appropriate format handler (raw, text, summary, or markdown).
 * param: ctx - context for cancellation and timeout
 * param: params - must contain "url"; optionally "format", "focus", "method", "body", "headers"
 * return: extracted content string with HTTP status, or error on invalid URL or request failure
 */
// Execute satisfies the Tool interface for callers outside the DAG. The
// dispatcher prefers ExecuteTyped and keeps the envelope, so the page text is
// never spliced to fit a character cap and ${node.X.title} still resolves.
func (w *WebFetch) Execute(ctx context.Context, params map[string]any) (string, error) {
	return toolapi.StringResult(w.ExecuteTyped(ctx, params))
}

func (w *WebFetch) ExecuteTyped(ctx context.Context, params map[string]any) (toolapi.ToolMessage, error) {
	rawURL, _ := params["url"].(string)
	if rawURL == "" {
		return toolapi.ToolMessage{}, fmt.Errorf("web_fetch: url is required")
	}

	format, _ := params["format"].(string)
	if format == "" {
		format = "markdown"
	}

	// Validate URL
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return toolapi.ToolMessage{}, fmt.Errorf("web_fetch: invalid URL %q (must start with http:// or https://)", rawURL)
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
		return toolapi.ToolMessage{}, fmt.Errorf("web_fetch: %w", err)
	}
	// Present a coherent desktop-Chrome fingerprint. These headers must stay
	// internally consistent (UA ↔ sec-ch-ua ↔ platform) — anti-bot filters flag
	// mismatched sets, so we set them as one block rather than a lone UA. The
	// referer is the one piece the planner knows from context (where the link
	// came from); a caller-supplied `headers` value still overrides any of these.
	referer, _ := params["referer"].(string)
	setBrowserHeaders(req, referer)
	if headers, ok := params["headers"].(map[string]any); ok {
		for k, v := range headers {
			if vs, ok := v.(string); ok {
				req.Header.Set(k, vs)
			}
		}
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return toolapi.ToolMessage{}, fmt.Errorf("web_fetch: %w", err)
	}
	defer resp.Body.Close()

	// How much of the body is read at all.
	//
	// This was 256KB, sized for what HTML extraction needs, from when extraction
	// was the only thing done with a body. It is now also the document that gets
	// kept, and a cap sized for reading the top of a page silently made the kept
	// file the top of the page too — a caller sent to the file for the rest of a
	// document found the same beginning again, one directory along.
	//
	// So the cap is what this deployment keeps. A binary a plugin can decode
	// still reads to a fixed ceiling of its own, because a decoder wants the
	// whole file and its size has nothing to do with how much of a page is worth
	// keeping.
	ctype := resp.Header.Get("Content-Type")
	readCap := int64(w.limits.MaxBodyBytes)
	if toolapi.HasBinaryDecoder(ctype) || looksLikePDFURL(rawURL) {
		if binaryCap := int64(16 * 1024 * 1024); binaryCap > readCap {
			readCap = binaryCap
		}
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, readCap))
	if err != nil {
		return toolapi.ToolMessage{}, fmt.Errorf("web_fetch: read body: %w", err)
	}

	status := fmt.Sprintf("HTTP %d %s", resp.StatusCode, resp.Status)

	// The page is kept first, before anything is decided about what to return
	// inline. Every outcome below then carries a path, so a caller that asked
	// for the wrong shape — or got a page that would not extract — still has the
	// document and can read, search or parse it in a later step, with none of
	// the limits that apply to a result travelling into a prompt.
	//
	// A failure to write is reported on the result and does not fail the fetch:
	// the page was still read, and returning nothing because it could not be
	// filed would lose more than it saved.
	keptPath, keptBytes, keptCut, keepErr := w.keepBody(rawURL, bodyBytes)

	// Build the result, then stamp the fetched URL onto it (withURL) at a single
	// exit — so every outcome, especially a 404 or an empty page, records WHICH url
	// produced it. A bare "HTTP 404" with no url is un-debuggable in the trace.
	var out toolapi.ToolMessage

	// Binary decode (PDF etc.) — only for non-error responses; computed once.
	var decoded string
	var decFound bool
	var decErr error
	if resp.StatusCode < 400 {
		decoded, decFound, decErr = decodePageBinary(ctype, rawURL, bodyBytes)
	}

	switch {
	case resp.StatusCode >= 400:
		// Error responses: return raw truncated.
		body := string(bodyBytes)
		if len(body) > 2048 {
			body = body[:2048] + "..."
		}
		out, err = marshalFetchResult(fetchResult{Status: status, Content: body, Format: format})

	case decFound:
		// A plugin decoded a binary body (e.g. a PDF a search turned up).
		switch {
		case decErr != nil:
			out, err = marshalFetchResult(fetchResult{Status: status, Format: format, Note: "downloaded but could not decode: " + decErr.Error()})
		case strings.TrimSpace(decoded) == "":
			out, err = marshalFetchResult(fetchResult{Status: status, Format: format, Note: "downloaded a document with no extractable text (likely scanned or image-only)"})
		default:
			// This tool's own cap on a decoded document, and the first of four
			// between here and the model — see agent.maxToolResultLen for the
			// rest. Raising it usually changes nothing, because a later one
			// cuts first.
			if len(decoded) > 16000 {
				decoded = decoded[:16000] + "\n… (truncated)"
			}
			out, err = marshalFetchResult(fetchResult{Status: status, Content: decoded, Format: format})
		}

	default:
		switch format {
		case "text":
			out, err = w.formatText(ctx, status, rawURL, bodyBytes)
		case "extract", "summary":
			// "summary" is what this was called when it paraphrased. It never
			// did — its instruction has always been to extract — so the name is
			// kept working rather than breaking every caller that learned it.
			focus, _ := params["focus"].(string)
			out, err = w.formatExtract(ctx, status, rawURL, bodyBytes, focus)
		default: // markdown
			out, err = w.formatMarkdown(ctx, status, rawURL, bodyBytes)
		}
	}
	return withKept(rawURL, keptPath, keptBytes, keptCut, keepErr, out, err)
}

/*
 * withKept records where the page was kept, then stamps the URL.
 * desc: One exit, so every outcome carries both — a 404, a page that would not
 *       extract, and a clean read all say which URL produced them and where the
 *       body is. A caller reading a thin result can go to the file rather than
 *       fetching again.
 * param: rawURL - what was fetched.
 * param: path - where the body was written, or "" if it was not.
 * param: bytes - how much was written.
 * param: cut - whether the body was larger than the deployment allows.
 * param: keepErr - why nothing was written, if that is what happened.
 * param: m - the result so far.
 * param: err - the error so far.
 * return: the result with the fetch recorded on it.
 */
func withKept(rawURL, path string, bytes int, cut bool, keepErr error, m toolapi.ToolMessage, err error) (toolapi.ToolMessage, error) {
	if err != nil {
		return withURL(rawURL, m, err)
	}
	obj := map[string]any{}
	if len(m.Data) > 0 {
		_ = json.Unmarshal(m.Data, &obj)
	}
	switch {
	case path != "":
		obj["full_content_path"] = path
		obj["bytes"] = bytes
		if cut {
			obj["body_truncated"] = true
		}
	case keepErr != nil:
		// Said out loud rather than left absent: a caller that expected a path
		// and finds none should be told the page was read and not filed, not
		// left to conclude the tool does not do that.
		obj["kept"] = "the page was read but could not be written: " + keepErr.Error()
	}
	if len(obj) > 0 {
		if b, mErr := json.Marshal(obj); mErr == nil {
			m.Data = b
		}
	}
	return withURL(rawURL, m, err)
}

// withURL stamps the fetched URL onto a fetch-result envelope: into Data always,
// and into Detail on any non-ok outcome — so the trace shows exactly WHICH url
// produced a 404 or an empty page instead of an anonymous error.
func withURL(rawURL string, m toolapi.ToolMessage, err error) (toolapi.ToolMessage, error) {
	if err != nil || rawURL == "" {
		return m, err
	}
	obj := map[string]any{}
	if len(m.Data) > 0 {
		_ = json.Unmarshal(m.Data, &obj)
	}
	// The URL that was asked for, echoed onto every result so a reader can say which
	// page it came from. It was set here and declared nowhere, which made this the
	// one tool returning a field its schema denied having — the description says in
	// capitals that this tool produces no URLs, and meant no *discovered* ones. The
	// declaration now names it and says which kind it is.
	obj["url"] = rawURL
	if b, e := json.Marshal(obj); e == nil {
		m.Data = b
	}
	if m.Status != toolapi.StatusOK && !strings.Contains(m.Detail, rawURL) {
		if m.Detail == "" {
			m.Detail = rawURL
		} else {
			m.Detail = m.Detail + " — " + rawURL
		}
	}
	return m, nil
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

func marshalFetchResult(r fetchResult) (toolapi.ToolMessage, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return toolapi.ToolMessage{}, err
	}
	// Map the fetch outcome onto the uniform tool envelope: HTTP >= 400 → error;
	// no content → empty; otherwise ok. The full fetchResult rides in Data so
	// ${node.X.title/.status/.format} keep resolving; the page text becomes the
	// evidence Content.
	//
	// Empty content is empty whether or not a reader left a Note. It used to
	// need both, and the readers that set one are the summary and document
	// paths — so a page fetched as markdown with nothing readable in it came
	// back ok with an empty Content, and a later stage could not tell a page
	// that held nothing from a page it had successfully read. The Note is the
	// better detail when there is one, because it says which way the page was
	// unreadable.
	msg := toolapi.ToolMessage{Type: "page", Data: data}
	switch {
	case strings.HasPrefix(r.Status, "HTTP 4") || strings.HasPrefix(r.Status, "HTTP 5"):
		msg.Status = toolapi.StatusError
		msg.Detail = r.Status
	case strings.TrimSpace(r.Content) == "":
		msg.Status = toolapi.StatusEmpty
		msg.Detail = r.Note
		if msg.Detail == "" {
			msg.Detail = "no readable content at that URL"
			if r.Status != "" {
				msg.Detail += " (" + r.Status + ")"
			}
		}
	default:
		msg.Status = toolapi.StatusOK
		msg.Content = r.Content
	}
	return msg, nil
}

// primaryContent returns a reader PLUGIN's extraction of the URL when one is
// enabled. An enabled reader plugin is the PRIMARY reader for web_fetch — it can
// render JS and extract cleanly, so it's used for EVERY page, not just as a last
// resort. (A JS/SPA page usually returns >200 chars of static nav/boilerplate, so
// the old "readability came back thin" trigger never fired on exactly the pages the
// plugin exists for.) ok=false when no plugin is registered or it returned nothing,
// so the caller falls back to built-in readability.
func primaryContent(ctx context.Context, rawURL string) (string, bool) {
	if txt, ok, _ := toolapi.ReaderFallback(ctx, rawURL); ok {
		if t := strings.TrimSpace(txt); len(t) >= 200 {
			return t, true
		}
	}
	return "", false
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
func (w *WebFetch) formatMarkdown(ctx context.Context, status, rawURL string, body []byte) (toolapi.ToolMessage, error) {
	// An enabled reader plugin is the primary reader.
	if txt, ok := primaryContent(ctx, rawURL); ok {
		// This tool's own cap on a reader plugin's markdown. The first of four
		// — see agent.maxToolResultLen for where the others cut.
		if len(txt) > 12000 {
			txt = txt[:12000] + "\n... (truncated)"
		}
		return marshalFetchResult(fetchResult{Status: status, Content: txt, Format: "markdown"})
	}

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
func (w *WebFetch) formatText(_ context.Context, status, rawURL string, body []byte) (toolapi.ToolMessage, error) {
	text := stripHTML(string(body))
	if len(text) > 8192 {
		text = text[:8192] + "\n... (truncated)"
	}
	return marshalFetchResult(fetchResult{Status: status, Content: text, Format: "text"})
}

/*
 * formatExtract reads the page and returns the parts of it that match a focus,
 * word for word.
 * desc: It does not paraphrase, and never did — its instruction has always been
 *       to extract, keeping exact names, numbers and dates. What changed is how
 *       much of the page it sees: it used to be handed the first sixteen
 *       thousand characters and asked about the whole document, so on anything
 *       longer it answered about the beginning and said nothing about the rest.
 *
 *       Now the page is split into pieces sized from the model that will read
 *       them, and read until the deployment's budget for one page is spent. The
 *       result says how many pieces there were and how many were read, because
 *       a caller told it saw six of forty can ask for more or go to the file,
 *       and a caller told nothing assumes it saw everything.
 * param: ctx - context for cancellation
 * param: status - HTTP status line string
 * param: rawURL - the original URL for readability parsing
 * param: body - raw HTML body bytes
 * param: focus - what to look for; empty asks for what the page is about
 * return: the matching text, or the page as markdown when no model is available
 */
func (w *WebFetch) formatExtract(ctx context.Context, status, rawURL string, body []byte, focus string) (toolapi.ToolMessage, error) {
	if w.executor == nil {
		// No LLM available, fall back to markdown
		return w.formatMarkdown(ctx, status, rawURL, body)
	}

	// An enabled reader plugin is the PRIMARY reader; built-in readability is the
	// fallback used only when no plugin is registered.
	content := ""
	title := ""
	if txt, ok := primaryContent(ctx, rawURL); ok {
		content = txt
	} else {
		parsed, _ := url.Parse(rawURL)
		if parsed == nil {
			parsed = &url.URL{}
		}
		article, _ := readability.FromReader(strings.NewReader(string(body)), parsed)
		if article.TextContent != "" {
			content = article.TextContent
			title = article.Title
		} else {
			content = stripHTML(string(body))
		}
	}

	// If nothing extractable came back (interactive widget, JS-rendered SPA with no
	// reader plugin, login wall, paywall), bail with a clear "no content" signal
	// rather than feeding the summarizer an empty page (which used to yield an LLM
	// apology that callers treated as the page's content). Refuse to fabricate.
	if len(strings.TrimSpace(content)) < 200 {
		// Last resort: page metadata (OpenGraph / meta description) — the
		// crawler-facing summary a JS/walled page still embeds. The reader plugin,
		// if any, already ran above as the primary reader.
		if meta := extractMeta(string(body)); len(strings.TrimSpace(meta)) >= 120 {
			content = meta
		} else {
			return marshalFetchResult(fetchResult{
				Status:  status,
				Title:   title,
				Content: "",
				Format:  "extract",
				Note:    "no extractable content (likely JS-rendered, login-walled, or an interactive widget). Try a different URL — an API endpoint or a static documentation page.",
			})
		}
	}

	// Build the extraction prompt. Be explicit that it must take from the
	// supplied content only — no general-knowledge fallback. If the page
	// doesn't contain what was asked for, return the exact sentinel below.
	const noContentSentinel = "__NO_RELEVANT_CONTENT__"
	prompt := "Extract the key information from this web page content. Use ONLY what is present in the user message; do not draw on outside knowledge."
	if focus != "" {
		prompt = fmt.Sprintf("Extract the following from this web page: %s. Be specific — include exact numbers, names, prices, dates where available. Use ONLY what is present in the user message; do not draw on outside knowledge.", focus)
	}
	prompt += fmt.Sprintf("\n\nIf the supplied content does not contain what was asked for, reply with this single token and nothing else: %s", noContentSentinel)

	if title != "" {
		prompt += fmt.Sprintf("\n\nPage title: %s", title)
	}

	// How much of the page goes in one pass, and how long a reply may be, are
	// the reading model's to say — see readingWindow. What used to be here were
	// two numbers, 16000 and 1024, which on a large-window model wasted most of
	// it and on a long page answered about the first few pages only.
	perPass, replyTokens := w.readingWindow(len(prompt))
	chunks := splitForReading(content, perPass)
	readable := w.chunksAffordable(len(chunks), perPass)

	pieces := make([]string, 0, readable)
	var lastErr error
	for i := 0; i < readable; i++ {
		resp, err := w.executor.Complete(ctx, &llm.ChatRequest{
			Messages: []llm.Message{
				{Role: "system", Content: prompt},
				{Role: "user", Content: chunks[i]},
			},
			Temperature: 0.2,
			MaxTokens:   replyTokens,
		})
		if err != nil {
			lastErr = err
			break
		}
		if len(resp.Choices) == 0 {
			continue
		}
		got := strings.TrimSpace(resp.Choices[0].Message.Content)
		// A piece that holds none of what was asked for says so, and saying so
		// once per piece would drown the pieces that do.
		if got == "" || strings.Contains(got, noContentSentinel) {
			continue
		}
		pieces = append(pieces, got)
	}

	if lastErr != nil && len(pieces) == 0 {
		// The model could not be reached at all. Fall back to the page as text,
		// which is worth more than nothing and is what this did before.
		if len(content) > perPass {
			content = content[:perPass] + "..."
		}
		return marshalFetchResult(fetchResult{Status: status, Title: title, Content: content, Format: "extract"})
	}

	summary := strings.Join(pieces, "\n\n")
	if summary == "" {
		// Every piece read said the page does not hold this. Handled below by
		// the sentinel path, which retries once without the focus rather than
		// discarding a page that carries something else useful.
		summary = noContentSentinel
	}

	// What was actually read, so a caller is never left assuming it saw the
	// whole page when the budget stopped it partway.
	coverage := ""
	if readable < len(chunks) {
		coverage = fmt.Sprintf("read %d of %d parts of this page — the rest was not read; the whole page is at the path on this result", readable, len(chunks))
	}

	// Detect the explicit sentinel.
	if strings.Contains(summary, noContentSentinel) {
		// The FOCUSED extraction found no match — but the page may still carry
		// useful general content that the narrow focus rejected (common on analyst
		// and report pages: the body is there, it just doesn't phrase the exact
		// figure asked for). Retry ONCE without the focus before discarding a
		// content-bearing page, so a real source isn't thrown away over wording.
		if focus != "" && len(strings.TrimSpace(content)) >= 400 {
			if g := w.generalSummary(ctx, content, noContentSentinel); g != "" {
				return marshalFetchResult(fetchResult{
					Status: status, Title: title, Content: g, Format: "extract",
					Note: "the whole page, not the focus — the page did not contain what the focus asked for",
				})
			}
		}
		return marshalFetchResult(fetchResult{
			Status:  status,
			Title:   title,
			Content: "",
			Format:  "extract",
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
			Format:  "extract",
			Note:    "the model could not find the requested information on the page",
		})
	}

	return marshalFetchResult(fetchResult{
		Status: status, Title: title, Content: summary, Format: "extract",
		// Empty unless the budget stopped the reading short, in which case it
		// says so — a caller that believes it saw the whole page and did not is
		// the failure this whole path exists to end.
		Note: coverage,
	})
}

// generalSummary is the focus-free fallback: summarize whatever real content the
// page yielded so a fetched, content-bearing source isn't discarded just because a
// narrow focus didn't match. Returns "" if the model still finds nothing usable
// (the sentinel, a refusal, or empty) — the caller then reports "no content".
func (w *WebFetch) generalSummary(ctx context.Context, content, sentinel string) string {
	resp, err := w.executor.Complete(ctx, &llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "Summarize the key facts, figures, and findings on this web page. Use ONLY what is present in the user message; do not draw on outside knowledge. Reply with " + sentinel + " only if the page has no substantive content at all."}, // foreign-word-ok: model-facing text; what a model is asked for is not reworded to satisfy a vocabulary check
			{Role: "user", Content: content},
		},
		Temperature: 0.2,
		MaxTokens:   1024,
	})
	if err != nil || len(resp.Choices) == 0 {
		return ""
	}
	g := strings.TrimSpace(resp.Choices[0].Message.Content)
	if g == "" || strings.Contains(g, sentinel) || looksLikeSummarizerRefusal(g) {
		return ""
	}
	return g
}

// looksLikePDFURL reports whether the URL path ends in .pdf — a fallback for
// servers that send a PDF without a proper application/pdf Content-Type.
func looksLikePDFURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(u.Path), ".pdf")
}

// decodePageBinary tries a plugin-registered decoder for the response — first by
// Content-Type, then (when the type is missing/generic) by a .pdf URL. found is
// false when no decoder applies and web_fetch continues with HTML extraction.
func decodePageBinary(contentType, rawURL string, body []byte) (text string, found bool, err error) {
	if t, ok, e := toolapi.DecodeBinary(contentType, body); ok {
		return t, true, e
	}
	if looksLikePDFURL(rawURL) {
		return toolapi.DecodeBinary("application/pdf", body)
	}
	return "", false, nil
}

var (
	metaTagRe = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	attrRe    = regexp.MustCompile(`(?is)([a-z:_-]+)\s*=\s*"([^"]*)"`)
	titleRe   = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title>`)
)

// extractMeta pulls a page's crawler-facing summary — OpenGraph / standard meta
// description and the title — for when the main extraction came up empty. Many
// JS-rendered or lightly-walled pages still expose this for SEO, so it yields a
// real abstract (and source attribution) instead of "no content". Best-effort,
// dependency-free.
func extractMeta(htmlBody string) string {
	var parts []string
	if t := metaContent(htmlBody, "property", "og:title"); t != "" {
		parts = append(parts, "Title: "+t)
	} else if m := titleRe.FindStringSubmatch(htmlBody); m != nil {
		if t := strings.TrimSpace(html.UnescapeString(m[1])); t != "" {
			parts = append(parts, "Title: "+t)
		}
	}
	if d := metaContent(htmlBody, "property", "og:description"); d != "" {
		parts = append(parts, d)
	} else if d := metaContent(htmlBody, "name", "description"); d != "" {
		parts = append(parts, d)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

// metaContent finds the first <meta> tag whose attr equals val (e.g.
// property="og:description") and returns its unescaped content attribute.
func metaContent(htmlBody, attr, val string) string {
	for _, tag := range metaTagRe.FindAllString(htmlBody, -1) {
		attrs := map[string]string{}
		for _, m := range attrRe.FindAllStringSubmatch(tag, -1) {
			attrs[strings.ToLower(m[1])] = m[2]
		}
		if strings.EqualFold(attrs[attr], val) {
			return strings.TrimSpace(html.UnescapeString(attrs["content"]))
		}
	}
	return ""
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

var _ toolapi.Tool = (*WebFetch)(nil)

// ── Keeping the page ────────────────────────────────────────────────────────

/*
 * keepBody writes a fetched body under the workspace and returns its path.
 * desc: Every fetch does this, whatever shape was asked for inline. It is the
 *       whole reason a caller can recover from asking for the wrong one: the
 *       document is on disk, and a later step reads, searches or parses it with
 *       none of the limits that apply to something travelling into a prompt.
 *
 *       A body over the deployment's ceiling is written up to it and reported
 *       as cut. Silently keeping part of a document that a caller believes is
 *       whole is the failure this exists to prevent, so it is never silent.
 * param: rawURL - what was fetched, used to name the file.
 * param: body - the bytes as they arrived.
 * return: the path, how many bytes were written, whether it was cut, and any
 *         error — an error here is not fatal to the fetch and the caller says so.
 */
func (w *WebFetch) keepBody(rawURL string, body []byte) (path string, written int, truncated bool, err error) {
	if w.workspace == "" {
		return "", 0, false, nil
	}

	keep := body
	if len(keep) > w.limits.MaxBodyBytes {
		keep = keep[:w.limits.MaxBodyBytes]
		truncated = true
	}

	dir := filepath.Join(w.workspace, "fetched")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, truncated, err
	}

	name := fetchFileName(rawURL)
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, keep, 0o644); err != nil {
		return "", 0, truncated, err
	}

	// Relative to the workspace, because that is the shape every other tool
	// takes a path in: a later file_read or bash step is given a path inside
	// the sandbox, not one that only means something on this machine.
	if rel, rerr := filepath.Rel(w.workspace, full); rerr == nil {
		return rel, len(keep), truncated, nil
	}
	return full, len(keep), truncated, nil
}

/*
 * fetchFileName names a kept body after where it came from.
 * desc: Host and path, with everything that is not a letter, digit, dash, dot
 *       or underscore replaced — so the name says which page it holds — plus a
 *       timestamp, so fetching one URL twice keeps both rather than one
 *       overwriting the other mid-run.
 * param: rawURL - the URL fetched.
 * return: a file name, never empty and never a path.
 */
func fetchFileName(rawURL string) string {
	stem := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		stem = u.Host + u.Path
	}
	var b strings.Builder
	for _, r := range stem {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '.', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	stem = strings.Trim(b.String(), "_.")
	if stem == "" {
		stem = "page"
	}
	// Long URLs make unwieldy names and can pass what a filesystem takes.
	if len(stem) > 120 {
		stem = stem[:120]
	}
	return fmt.Sprintf("%s_%d", stem, time.Now().UnixNano())
}

// ── How much a page is read in, and how much of it is read ──────────────────

// What a fetch falls back to when the model will not say how big its window is.
//
// One number, in one place, for one case: a provider that publishes no limits.
// It is deliberately the behaviour this tool had before it could ask, so an
// unknown model behaves exactly as everything did previously rather than
// differently and unpredictably.
const unknownModelWindowChars = 16000

// unknownModelReplyTokens is the same idea for the reply.
const unknownModelReplyTokens = 1024

// Characters per token, for turning a model's token window into a number of
// characters of page. Deliberately pessimistic: over-estimating the tokens in a
// piece of text makes the piece smaller and the reading slower, while
// under-estimating makes a request the model refuses.
const charsPerToken = 3

/*
 * readingWindow reports how much page goes into one pass and how long a reply
 * may be, from the model that will read it.
 * desc: The input window is what the model takes, less the instruction it is
 *       sent with and the reply it has to have room for. The reply is the
 *       model's own output limit — an extraction that quotes a list of names or
 *       parameters runs past a small one and is cut mid-item.
 *
 *       Both fall back to this file's one unknown-model number when the client
 *       carries no limits or does not know the model.
 * param: promptChars - how much the instruction itself takes.
 * return: characters of page per pass, and tokens of reply.
 */
func (w *WebFetch) readingWindow(promptChars int) (perPassChars, replyTokens int) {
	ctxTokens, outTokens := 0, 0
	if w.executor != nil {
		ctxTokens, outTokens = w.executor.WindowFor()
	}

	replyTokens = outTokens
	if replyTokens <= 0 {
		replyTokens = unknownModelReplyTokens
	}

	if ctxTokens <= 0 {
		return unknownModelWindowChars, replyTokens
	}

	// What is left of the window once the instruction and the reply have their
	// room. A tenth is held back so a request never lands exactly on the limit.
	spare := ctxTokens - replyTokens - (promptChars / charsPerToken)
	spare -= ctxTokens / 10
	if spare <= 0 {
		return unknownModelWindowChars, replyTokens
	}

	perPassChars = spare * charsPerToken
	if perPassChars < 2000 {
		// A window this small reads nothing useful in a pass; the fallback is
		// closer to workable than a sliver would be.
		perPassChars = 2000
	}
	return perPassChars, replyTokens
}

/*
 * chunksAffordable reports how many pieces the deployment's budget pays for.
 * desc: A token budget rather than a count, so a model with a large window
 *       reads a long page in one pass and a small one in several, and neither
 *       number is written down. Always at least one: a budget that pays for no
 *       reading at all is a misconfiguration, and returning nothing would hide
 *       it behind an empty result.
 * param: total - how many pieces the page came to.
 * param: perPassChars - how much page each piece holds.
 * return: how many to read.
 */
func (w *WebFetch) chunksAffordable(total, perPassChars int) int {
	if total <= 1 {
		return total
	}
	affordable := w.limits.ExtractTokenBudget / (perPassChars / charsPerToken)
	if affordable < 1 {
		affordable = 1
	}
	if affordable > total {
		affordable = total
	}
	return affordable
}

/*
 * splitForReading cuts text into pieces of at most size, on paragraph breaks
 * where it can.
 * desc: Cutting mid-sentence loses the sentence for both pieces, and what is
 *       being looked for is often a name or a figure inside one. Paragraph
 *       breaks are the cheapest boundary that does not need to understand the
 *       text.
 * param: text - the page.
 * param: size - the most one piece may hold, in characters.
 * return: the pieces, in order; one piece when the text already fits.
 */
func splitForReading(text string, size int) []string {
	if size <= 0 || len(text) <= size {
		return []string{text}
	}
	var out []string
	for len(text) > size {
		cut := strings.LastIndex(text[:size], "\n\n")
		if cut < size/2 {
			// No paragraph break in the back half — take a line break, then
			// give up and cut where the size says.
			if nl := strings.LastIndex(text[:size], "\n"); nl > size/2 {
				cut = nl
			} else {
				cut = size
			}
		}
		out = append(out, strings.TrimSpace(text[:cut]))
		text = text[cut:]
	}
	if rest := strings.TrimSpace(text); rest != "" {
		out = append(out, rest)
	}
	return out
}
