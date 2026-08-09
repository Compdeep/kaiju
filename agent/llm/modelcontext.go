package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Asking the endpoint how big a model's context window is.
//
// A catalog can only answer for models someone put in it, which leaves out
// every self-hosted deployment. The endpoint serving the model always knows,
// and some report it: vLLM publishes max_model_len on GET /v1/models, and
// OpenRouter's own API publishes context_length there.
//
// Plenty do not. OpenAI's /v1/models carries id, object, created and owned_by
// and nothing about size; Anthropic publishes no figure; Ollama uses a different
// API entirely. So this is best effort by construction: it reports zero rather
// than guessing, and a caller must have a plan for zero. It is a metadata
// request — no prompt, no completion, no tokens.

// modelsTimeout bounds the probe. It runs at startup, where a slow or wrong
// endpoint must not hold the process, and the answer is optional anyway.
const modelsTimeout = 10 * time.Second

// modelEntry is the part of a /v1/models entry worth reading. Both spellings
// appear in the wild and mean the same thing.
type modelEntry struct {
	ID            string `json:"id"`
	MaxModelLen   int    `json:"max_model_len"`
	ContextLength int    `json:"context_length"`
}

func (m modelEntry) window() int {
	if m.MaxModelLen > 0 {
		return m.MaxModelLen
	}
	return m.ContextLength
}

// modelsURL is chatURL's counterpart for the model listing.
func (c *Client) modelsURL() string {
	ep := strings.TrimRight(c.endpoint, "/")
	if strings.HasSuffix(ep, "/v1") {
		return ep + "/models"
	}
	return ep + "/v1/models"
}

/*
 * ModelContext reports a model's context window in tokens, as the endpoint
 * states it.
 * desc: One GET on the model listing. Returns the window for the named model,
 *       or for this client's own model when the name is empty.
 *
 *       Zero with no error means the endpoint answered and said nothing about
 *       size — the common case, and not a fault. Zero with an error means the
 *       listing could not be read. Either way a caller must be able to proceed
 *       without the number; nothing here invents one.
 * param: ctx - bounded internally to modelsTimeout, whichever expires first.
 * param: model - the model id to look up; empty uses the client's own.
 * return: the window in tokens, and why it is zero when it is.
 */
func (c *Client) ModelContext(ctx context.Context, model string) (int, error) {
	if c == nil {
		return 0, fmt.Errorf("no client")
	}
	if model == "" {
		model = c.model
	}

	ctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.modelsURL(), nil)
	if err != nil {
		return 0, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("model listing: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("model listing: status %d", resp.StatusCode)
	}

	var body struct {
		Data []modelEntry `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("model listing: %w", err)
	}

	// Exact id first. A deployment often serves one model under a name that does
	// not match what the caller configured — a path, or a shortened tag — so a
	// single-entry listing is taken as the answer rather than reported as a miss.
	for _, m := range body.Data {
		if m.ID == model {
			return m.window(), nil
		}
	}
	if len(body.Data) == 1 {
		return body.Data[0].window(), nil
	}
	return 0, fmt.Errorf("model listing: %q not among the %d models offered", model, len(body.Data))
}
