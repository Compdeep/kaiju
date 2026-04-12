package db

import (
	"encoding/json"
	"fmt"
)

/*
 * ClearanceEndpoint is the DB representation of an external authorization endpoint.
 * desc: Stores the URL, timeout, and headers for a per-tool external clearance service used by the IGX gate
 */
type ClearanceEndpoint struct {
	ToolName  string            `json:"tool_name"`
	URL       string            `json:"url"`
	TimeoutMs int              `json:"timeout_ms"`
	Headers   map[string]string `json:"headers,omitempty"`
}

/*
 * UpsertClearanceEndpoint creates or updates a clearance endpoint.
 * desc: Inserts or replaces a clearance endpoint row, defaulting timeout to 2000ms if non-positive
 * param: ep - the ClearanceEndpoint to upsert (ToolName, URL, TimeoutMs, and Headers fields are used)
 * return: error if the upsert fails, nil on success
 */
func (d *DB) UpsertClearanceEndpoint(ep ClearanceEndpoint) error {
	headersJSON, _ := json.Marshal(ep.Headers)
	if ep.TimeoutMs <= 0 {
		ep.TimeoutMs = 2000
	}
	_, err := d.conn.Exec(
		`INSERT OR REPLACE INTO clearance_endpoints (tool_name, url, timeout_ms, headers) VALUES (?, ?, ?, ?)`,
		ep.ToolName, ep.URL, ep.TimeoutMs, string(headersJSON),
	)
	if err != nil {
		return fmt.Errorf("db: upsert clearance endpoint: %w", err)
	}
	return nil
}

/*
 * GetClearanceEndpoint returns the clearance endpoint for a tool.
 * desc: Looks up a single clearance endpoint row by tool name
 * param: toolName - the tool name to look up
 * return: pointer to the ClearanceEndpoint and nil error, or nil and an error if not found
 */
func (d *DB) GetClearanceEndpoint(toolName string) (*ClearanceEndpoint, error) {
	row := d.conn.QueryRow(
		`SELECT tool_name, url, timeout_ms, headers FROM clearance_endpoints WHERE tool_name = ?`, toolName,
	)
	var ep ClearanceEndpoint
	var headersJSON string
	if err := row.Scan(&ep.ToolName, &ep.URL, &ep.TimeoutMs, &headersJSON); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(headersJSON), &ep.Headers)
	return &ep, nil
}

/*
 * ListClearanceEndpoints returns all configured clearance endpoints.
 * desc: Queries all clearance endpoint rows ordered by tool name
 * return: slice of all ClearanceEndpoints and nil error, or nil and an error on query failure
 */
func (d *DB) ListClearanceEndpoints() ([]ClearanceEndpoint, error) {
	rows, err := d.conn.Query(`SELECT tool_name, url, timeout_ms, headers FROM clearance_endpoints ORDER BY tool_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var endpoints []ClearanceEndpoint
	for rows.Next() {
		var ep ClearanceEndpoint
		var headersJSON string
		if err := rows.Scan(&ep.ToolName, &ep.URL, &ep.TimeoutMs, &headersJSON); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(headersJSON), &ep.Headers)
		endpoints = append(endpoints, ep)
	}
	return endpoints, nil
}

/*
 * DeleteClearanceEndpoint removes a clearance endpoint.
 * desc: Deletes the clearance endpoint row matching the given tool name
 * param: toolName - the tool name whose endpoint to delete
 * return: error if the endpoint is not found or the query fails, nil on success
 */
func (d *DB) DeleteClearanceEndpoint(toolName string) error {
	result, err := d.conn.Exec(`DELETE FROM clearance_endpoints WHERE tool_name = ?`, toolName)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("db: clearance endpoint for %q not found", toolName)
	}
	return nil
}
