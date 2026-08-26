package agent

import (
	"fmt"
	"github.com/Compdeep/kaiju/agent/toolapi"
	"strconv"
	"strings"
)

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
 * TruncateLog strips newlines then truncates, naming the cut.
 * desc: Replaces newlines with spaces before truncating, producing single-line
 *       entries.
 *
 *       The marker says what happened and how much is missing, because 46 of
 *       these calls end up in a prompt and a model cannot tell a bare "..." from
 *       text that ends in one. Three times in a single run a stage read this
 *       function's own marker as evidence that the DATA was incomplete:
 *
 *         - a 74-byte process list, whole, reported as output that had been cut
 *         - a 2,772-byte script, whole, diagnosed by Holmes as "truncated during
 *           generation" — Holmes had cut it here itself, at 1500, then read the
 *           marker back on its next iteration and promoted it to a high-
 *           confidence root cause
 *         - and the repair that followed, of a file that was never broken
 *
 *       "…[cut 1500/2772]" cannot be read as the end of a document, and it names
 *       the whole so a reader can tell "this is all of it" from "this is the
 *       start of it". Kept short because these also render as one-line trace
 *       summaries, where a sentence would be longer than the text it marks.
 * param: s - the string to truncate
 * param: n - maximum length before truncation
 * return: the single-line string, with the cut named when one was made
 */
func (textNS) TruncateLog(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + truncationMark(len(s), n)
}

/*
 * truncationMark says a cut was made here, and how big the whole was.
 * desc: One wording for every cap, so a reader learns it once. Names the total
 *       so a stage can tell "this is all of it" from "this is the start of it" —
 *       which is the distinction every one of these mistakes turned on.
 * param: total - the size of the whole string, in characters.
 * param: kept - how much survived.
 * return: the marker, including its leading space.
 */
func truncationMark(total, kept int) string {
	return fmt.Sprintf(" …[cut %d/%d]", kept, total)
}

/*
 * TailTruncate keeps the LAST n characters of a string with a leading marker.
 * desc: Use for log-shaped content where the most recent entries are at the
 *       bottom (stderr dumps, error logs, worklog tails). Newlines are
 *       preserved so multi-line errors stay readable.
 * param: s - the string to truncate
 * param: n - maximum length to keep from the tail
 * return: the original or tail-truncated string
 */
func (textNS) TailTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "...(earlier output truncated to the last " + strconv.Itoa(n) + " chars)\n" + s[len(s)-n:]
}

/*
 * TruncateEvidence caps ONE step's contribution to the evidence a prompt
 * carries — not the whole of it. A run with twenty steps sends twenty of these.
 *
 * The third of five caps between a tool and the model; see maxToolResultLen for
 * the order. For a tool that returns a typed envelope this is the FIRST one to
 * cut on the DAG path, because the dispatch cap skips those.
 *
 * TruncateEvidence caps a result string for LLM synthesis input.
 * desc: Truncates to 8000 chars with a synthesis-specific suffix. Full results are preserved on the Node.
 *       8000 (not 2048) so a multi-result web_search keeps ALL its URLs — a tight
 *       cap dropped later results' URLs, and a replan then hallucinated them.
 * param: s - the evidence string to truncate
 * return: the original or truncated string
 */
// TruncateEvidence cuts to the compiled default. It is the answer for a caller
// with no agent to ask; the gate calls TruncateEvidenceTo with the budget the
// deployed model's window allows.
func (t textNS) TruncateEvidence(s string) string {
	return t.TruncateEvidenceTo(s, toolapi.EvidenceBudget)
}

// TruncateEvidenceTo cuts one step's result to a given budget.
//
// The budget is a parameter rather than a constant because it depends on the
// model, and the graph holding a result does not know which model will read it.
// It was 8000 characters everywhere, chosen when a large window was 32K — see
// Agent.evidenceBudget.
func (textNS) TruncateEvidenceTo(s string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = toolapi.EvidenceBudget
	}
	if len(s) <= maxLen {
		return s
	}
	// Show head + tail so both the beginning and end are visible.
	// Avoids cutting mid-JSON or mid-sentence.
	headLen := maxLen * 2 / 3
	tailLen := maxLen / 3
	// The marker names which cap cut, and how big it was — a reader of a prompt
	// has to tell this one from the dispatch cap, a tool's own cap and the
	// gate's budget, and the size is no longer the same on every deployment.
	marker := fmt.Sprintf("\n\n...(middle truncated by the %d-char evidence cap)...\n\n", maxLen)
	return s[:headLen] + marker + s[len(s)-tailLen:]
}

/*
 * HeadTail keeps the first headN chars and last tailN chars of a string,
 * joining them with a separator. If the string fits within headN+tailN,
 * returns it unchanged. Generic version of TruncateEvidence.
 */
func (textNS) HeadTail(s string, headN, tailN int, sep ...string) string {
	if len(s) <= headN+tailN {
		return s
	}
	marker := "\n...\n"
	if len(sep) > 0 && sep[0] != "" {
		marker = sep[0]
	}
	return s[:headN] + marker + s[len(s)-tailN:]
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
