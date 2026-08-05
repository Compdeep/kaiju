package llm

import "strings"

// IsAuthFailure reports whether an error from a provider reflects a credential
// problem rather than a transient fault: a missing or wrong key, a model the
// key cannot reach, or an exhausted quota.
//
// The distinction matters because these are the errors that must NOT be
// retried. A run that retries a transient timeout is doing the right thing; a
// run that retries an invalid key spends its whole budget failing identically,
// and reports the last failure rather than the real one. Callers use this to
// stop early and say what is actually wrong.
//
// Matching is on the message text because providers disagree on everything
// else — status codes, error shapes, field names — and this package speaks to
// several. That makes it a heuristic, so it is deliberately narrow: every term
// here means a credential or entitlement problem in ordinary provider wording,
// and none of them appears in a transient failure.
func IsAuthFailure(errMsg string) bool {
	lower := strings.ToLower(errMsg)
	for _, term := range []string{
		"http 401",
		"http 403",
		"unauthorized",
		"forbidden",
		"invalid api key",
		"insufficient_quota",
		"insufficient credits",
		"authentication",
	} {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}
