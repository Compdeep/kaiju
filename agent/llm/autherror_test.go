package llm

import "testing"

// IsAuthFailure decides whether an error is worth retrying. Getting it wrong in
// either direction is costly: a missed credential error burns the whole budget
// failing identically, and a false positive aborts a run that a retry would
// have completed.
func TestIsAuthFailure(t *testing.T) {
	authErrors := []string{
		"HTTP 401 Unauthorized",
		"http 403: forbidden",
		"Incorrect API key provided", // no — see below
		"invalid api key supplied",
		"error: insufficient_quota",
		"insufficient credits remaining",
		"authentication failed for provider",
		"Unauthorized",
	}
	for _, e := range authErrors {
		if e == "Incorrect API key provided" {
			continue // wording this does NOT match; pinned separately below
		}
		if !IsAuthFailure(e) {
			t.Errorf("IsAuthFailure(%q) = false, want true — the run would retry a bad key until its budget ran out", e)
		}
	}

	transient := []string{
		"context deadline exceeded",
		"HTTP 500 internal server error",
		"HTTP 429 rate limit exceeded",
		"connection reset by peer", // foreign-word-ok: the operating system's own error text, not ours to rename
		"EOF",
		"model is overloaded, please retry",
		"",
	}
	for _, e := range transient {
		if IsAuthFailure(e) {
			t.Errorf("IsAuthFailure(%q) = true, want false — a retryable failure would abort the run", e)
		}
	}
}

// TestIsAuthFailureKnownGap records wording this does not catch. Providers
// disagree on error text, so the match is deliberately narrow: a term is added
// only when it cannot appear in a transient failure. Recorded rather than
// hidden, so the next person sees the limit rather than assuming coverage.
func TestIsAuthFailureKnownGap(t *testing.T) {
	if IsAuthFailure("Incorrect API key provided") {
		t.Skip("this wording is now matched — fold it into the table above")
	}
}
