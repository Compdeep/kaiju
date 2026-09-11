package llm

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
// Takes the message rather than the error because its one caller has only a
// message by the time it asks. A caller holding the error should ask Classify,
// which answers from the type where this package built it and falls back to the
// same terms where it did not — see failure.go.
func IsAuthFailure(errMsg string) bool {
	return classifyText(errMsg) == KindCredentials
}
