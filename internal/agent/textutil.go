package agent

import "strings"

/*
 * textNS provides namespaced text utility functions.
 * desc: Consolidates truncation, code fence stripping, and markdown parsing helpers used across the agent package.
 */
type textNS int

/*
 * Text is the namespace for text utility functions.
 * desc: Use Text.Truncate(), Text.TruncateLog(), etc. for consistent string manipulation.
 */
const Text textNS = 0

/*
 * Truncate shortens a string to n characters with an ellipsis suffix.
 * desc: Returns s unchanged if len(s) <= n, otherwise truncates and appends "..."
 * param: s - the string to truncate
 * param: n - maximum length before truncation
 * return: the original or truncated string
 */
func (textNS) Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

/*
 * TruncateLog strips newlines then truncates for log output.
 * desc: Replaces newlines with spaces before truncating, producing single-line log entries.
 * param: s - the string to truncate
 * param: n - maximum length before truncation
 * return: the single-line truncated string
 */
func (textNS) TruncateLog(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

/*
 * TruncateEvidence caps a result string for LLM synthesis input.
 * desc: Truncates to 2048 chars with a synthesis-specific suffix. Full results are preserved on the Node.
 * param: s - the evidence string to truncate
 * return: the original or truncated string
 */
func (textNS) TruncateEvidence(s string) string {
	const maxEvidenceLen = 2048
	if len(s) <= maxEvidenceLen {
		return s
	}
	return s[:maxEvidenceLen] + "\n... (truncated for synthesis)"
}

/*
 * StripCodeFence removes markdown code fences and extracts JSON content.
 * desc: Strips opening/closing ``` fences and locates the first JSON array or object in the string.
 * param: s - the string potentially wrapped in code fences
 * return: the extracted JSON content, trimmed
 */
func (textNS) StripCodeFence(s string) string {
	s = strings.TrimSpace(s)

	// Remove opening fence (```json or ```)
	if strings.HasPrefix(s, "```") {
		idx := strings.Index(s, "\n")
		if idx >= 0 {
			s = s[idx+1:]
		}
	}

	// Remove closing fence — only if it's on its own line (not inside code content)
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "```" {
			s = strings.Join(lines[:i], "\n")
			break
		}
	}

	s = strings.TrimSpace(s)

	// If the response doesn't start with [ or {, try to extract JSON from it.
	if !strings.HasPrefix(s, "[") && !strings.HasPrefix(s, "{") {
		bracketIdx := strings.Index(s, "[")
		braceIdx := strings.Index(s, "{")
		startIdx := -1
		if bracketIdx >= 0 && (braceIdx < 0 || bracketIdx < braceIdx) {
			startIdx = bracketIdx
		} else if braceIdx >= 0 {
			startIdx = braceIdx
		}
		if startIdx >= 0 {
			s = s[startIdx:]
		}
	}

	return strings.TrimSpace(s)
}

/*
 * ExtractSection pulls a markdown section from a body by heading.
 * desc: Returns the content between the heading and the next same-level heading (or end of body).
 * param: body - the full markdown body to search
 * param: heading - the heading to find (e.g. "## Planning Guidance")
 * return: the section content trimmed, or empty string if heading not found
 */
func (textNS) ExtractSection(body, heading string) string {
	idx := strings.Index(body, heading)
	if idx < 0 {
		return ""
	}
	section := body[idx+len(heading):]
	prefix := strings.TrimSpace(heading)
	level := 0
	for _, c := range prefix {
		if c == '#' {
			level++
		} else {
			break
		}
	}
	marker := "\n" + strings.Repeat("#", level) + " "
	if nextH := strings.Index(section, marker); nextH >= 0 {
		section = section[:nextH]
	}
	return strings.TrimSpace(section)
}
