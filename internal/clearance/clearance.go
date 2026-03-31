// Package clearance provides external authorization endpoint integration.
// Tools can have clearance endpoints configured — before execution, the gate
// calls the endpoint with the tool name, params, and user. The endpoint returns
// allow/deny. If no endpoint is configured, the tool executes freely.
// If the endpoint is unreachable or times out, execution is denied by default.
package clearance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// Endpoint defines an external authorization service for a tool.
type Endpoint struct {
	ToolName  string            `json:"tool_name"`
	URL       string            `json:"url"`
	TimeoutMs int              `json:"timeout_ms"` // default 2000
	Headers   map[string]string `json:"headers,omitempty"`
}

// Request is sent to the clearance endpoint.
type Request struct {
	Tool   string         `json:"tool"`
	Params map[string]any `json:"params"`
	User   string         `json:"user"`
}

// Response is expected from the clearance endpoint.
type Response struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason,omitempty"`
}

// Checker manages clearance endpoint lookups and authorization calls.
type Checker struct {
	endpoints map[string]Endpoint // tool name → endpoint
	client    *http.Client
}

// NewChecker creates a checker with no endpoints configured.
func NewChecker() *Checker {
	return &Checker{
		endpoints: make(map[string]Endpoint),
		client:    &http.Client{},
	}
}

// SetEndpoint registers or updates a clearance endpoint for a tool.
func (c *Checker) SetEndpoint(ep Endpoint) {
	if ep.TimeoutMs <= 0 {
		ep.TimeoutMs = 2000
	}
	c.endpoints[ep.ToolName] = ep
}

// RemoveEndpoint removes a clearance endpoint for a tool.
func (c *Checker) RemoveEndpoint(toolName string) {
	delete(c.endpoints, toolName)
}

// HasEndpoint returns true if a clearance endpoint is configured for this tool.
func (c *Checker) HasEndpoint(toolName string) bool {
	_, ok := c.endpoints[toolName]
	return ok
}

// ListEndpoints returns all configured endpoints.
func (c *Checker) ListEndpoints() []Endpoint {
	out := make([]Endpoint, 0, len(c.endpoints))
	for _, ep := range c.endpoints {
		out = append(out, ep)
	}
	return out
}

// Check calls the clearance endpoint for the given tool.
// Returns nil if no endpoint is configured (default allow).
// Returns error if endpoint denies, is unreachable, or times out.
func (c *Checker) Check(ctx context.Context, toolName string, params map[string]any, user string) error {
	ep, ok := c.endpoints[toolName]
	if !ok {
		return nil // no endpoint configured — default allow
	}

	reqBody := Request{
		Tool:   toolName,
		Params: params,
		User:   user,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("clearance: marshal request: %w", err)
	}

	timeout := time.Duration(ep.TimeoutMs) * time.Millisecond
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "POST", ep.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("clearance: denied (bad endpoint URL)")
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range ep.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		log.Printf("[clearance] endpoint %s unreachable for %s: %v — denying", ep.URL, toolName, err)
		return fmt.Errorf("clearance: denied (endpoint unreachable)")
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return fmt.Errorf("clearance: denied (read error)")
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("clearance: denied (endpoint returned %d)", resp.StatusCode)
	}

	var result Response
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("clearance: denied (invalid response)")
	}

	if !result.Allow {
		reason := result.Reason
		if reason == "" {
			reason = "denied by clearance endpoint"
		}
		return fmt.Errorf("clearance: %s — %s", toolName, reason)
	}

	return nil
}
