package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Naming the ways a model call comes back without a usable answer.
//
// There were three copies of this question, each a substring search over the
// error's English: one asking whether a key is bad, one whether the provider is
// down, one whether the other end asked to be left alone. They matched
// different terms, so they disagreed — and a sentence is not a type, so nothing
// could tell them they did.
//
// The sentences stay. They are the last resort for an error that arrived from
// somewhere this package did not build, and Classify falls back to them.

// Kind is what happened to a call.
type Kind int

const (
	// KindNone is a call that produced a usable answer, or an error this
	// package cannot place.
	KindNone Kind = iota
	// KindTransport never reached the provider: connection refused, DNS, TLS.
	KindTransport
	// KindRateLimited is the other end asking to be left alone.
	KindRateLimited
	// KindUpstream reached the provider and the provider could not serve it: a
	// 5xx, a 200 carrying an error object, a 200 with no choices at all.
	KindUpstream
	// KindCredentials is a key problem: missing, wrong, not entitled to the
	// model, or out of quota. Never retried — a run that retries an invalid key
	// spends its whole budget failing identically and then reports the last
	// failure rather than the real one.
	KindCredentials
	// KindTimeout is our own deadline, not the provider's.
	KindTimeout
	// KindEmpty is a reply that arrived carrying neither content nor a tool
	// call. The call succeeded and produced nothing.
	KindEmpty
	// KindTruncated stopped at the token cap with something written. An answer,
	// not a failure — what to do about it belongs to whoever asked.
	KindTruncated
)

func (k Kind) String() string {
	switch k {
	case KindTransport:
		return "transport"
	case KindRateLimited:
		return "rate limited"
	case KindUpstream:
		return "upstream"
	case KindCredentials:
		return "credentials"
	case KindTimeout:
		return "timed out"
	case KindEmpty:
		return "empty reply"
	case KindTruncated:
		return "truncated"
	}
	return ""
}

// CallError is a call that ended badly, with what is known about how.
//
// Body carries a bounded slice of the response for the endings where it is the
// only evidence — a 200 with no choices and no error object leaves nothing else
// behind, and it is unrecoverable after the fact.
type CallError struct {
	Kind       Kind
	Status     int           // the HTTP status, 0 when there was none
	RetryAfter time.Duration // what the provider asked for, 0 when it did not say
	Body       string
	Err        error // the underlying failure, where there was one
}

// Error is the sentence the call would have produced without any of this.
//
// Deliberately unchanged: the text is what an operator reads in a log and what
// an error arriving from outside this package is still matched on. The Kind is
// metadata beside it, not a rewrite of it.
func (e *CallError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.Status != 0 {
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body)
	}
	return e.Kind.String()
}

func (e *CallError) Unwrap() error { return e.Err }

/*
 * Classify reports what happened to a call.
 * desc: The typed answer where this package built the error, and the sentence
 *       otherwise — an application's transport, a tool's own HTTP client and a
 *       provider SDK all produce errors this package never saw.
 *
 *       Order matters in the fallback: a 403 is both a status and a credential
 *       problem, and only one of those answers is useful.
 * param: err - the error, or nil.
 * return: the kind, KindNone for nil and for anything unrecognised.
 */
func Classify(err error) Kind {
	if err == nil {
		return KindNone
	}
	var ce *CallError
	if errors.As(err, &ce) {
		return ce.Kind
	}
	if errors.Is(err, ErrReplyTruncated) {
		return KindTruncated
	}
	// Our own cancellation is not the provider's fault, and not a failure this
	// layer acts on.
	if errors.Is(err, context.Canceled) {
		return KindNone
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return KindTimeout
	}
	return classifyText(err.Error())
}

// The terms, in one place. Each was one of three separate lists, and every one
// of them means what it is filed under in ordinary provider wording.
var (
	credentialTerms = []string{
		"http 401", "http 403", "unauthorized", "forbidden",
		"invalid api key", "insufficient_quota", "insufficient credits",
		"authentication",
	}
	rateLimitTerms = []string{
		"http 429", "too many requests", "rate limit",
	}
	upstreamTerms = []string{
		"provider returned an error with http 200",
		"provider returned no choices",
		"context deadline exceeded",
	}
)

// classifyText is the last resort: what the sentence says.
func classifyText(msg string) Kind {
	lower := strings.ToLower(msg)
	// Credentials first. A 403 matches the status scan below as well, and only
	// this answer tells a caller to stop rather than to try again.
	if containsAny(lower, credentialTerms) {
		return KindCredentials
	}
	if containsAny(lower, rateLimitTerms) {
		return KindRateLimited
	}
	if containsAny(lower, upstreamTerms) {
		return KindUpstream
	}
	if status := statusIn(lower); status >= 500 && status <= 599 {
		return KindUpstream
	}
	return KindNone
}

func containsAny(lower string, terms []string) bool {
	for _, t := range terms {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

// statusIn reads the code out of a "HTTP nnn" message, and 0 when there is
// none. The scan this replaces tried a hundred codes in turn.
func statusIn(lower string) int {
	i := strings.Index(lower, "http ")
	if i < 0 || len(lower) < i+8 {
		return 0
	}
	n, err := strconv.Atoi(lower[i+5 : i+8])
	if err != nil {
		return 0
	}
	return n
}

/*
 * Backoff is how long a caller should wait before asking again, if at all.
 * desc: Two endings are the other end saying it is busy, and rerunning them at
 *       once is the one thing certain not to work — measured on a 429 rerun
 *       51ms later, which returned 429 again.
 *
 *       The provider's own Retry-After wins where it sent one; otherwise a
 *       short default, because a wait nobody chose is better than none.
 * param: err - the error from the call.
 * return: the wait, and whether waiting is the right answer at all.
 */
func Backoff(err error) (time.Duration, bool) {
	var ce *CallError
	if errors.As(err, &ce) {
		if ce.Kind != KindRateLimited && ce.Status != http.StatusServiceUnavailable {
			return 0, false
		}
		if ce.RetryAfter > 0 {
			return ce.RetryAfter, true
		}
		return defaultBackoff, true
	}
	if Classify(err) == KindRateLimited || strings.Contains(strings.ToLower(err.Error()), "http 503") {
		return defaultBackoff, true
	}
	return 0, false
}

// defaultBackoff is the wait for a provider that said it was busy without
// saying for how long. Long enough that the second attempt is not the first one
// again, short enough that a caller waiting on an answer is not abandoned.
const defaultBackoff = 2 * time.Second

/*
 * retryAfterHeader reads the provider's own Retry-After.
 * desc: Both spellings the standard allows — a count of seconds, or an HTTP
 *       date. An unreadable value is no value rather than an error: the caller
 *       falls back to its own wait.
 * param: h - the response headers.
 * return: the wait the provider asked for, or 0.
 */
func retryAfterHeader(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

/*
 * httpFailure names what a non-200 response was.
 * desc: The status decides, except where it does not: quota problems arrive as
 *       402 and sometimes as 400, and only the body says so. Built here rather
 *       than at each return so the three send paths cannot disagree.
 * param: status - the HTTP status.
 * param: hdr - the response headers, for Retry-After.
 * param: body - the response body, already bounded by the caller.
 * return: the failure.
 */
func httpFailure(status int, hdr http.Header, body string) *CallError {
	kind := KindUpstream
	switch {
	case status == http.StatusTooManyRequests:
		kind = KindRateLimited
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		kind = KindCredentials
	case status >= 500:
		kind = KindUpstream
	default:
		// Every other 4xx. The body is the only thing that can tell a quota
		// problem from a malformed request.
		kind = classifyText(body)
	}
	return &CallError{
		Kind:       kind,
		Status:     status,
		RetryAfter: retryAfterHeader(hdr),
		Body:       body,
		Err:        fmt.Errorf("HTTP %d: %s", status, body),
	}
}

// transportFailure is a call that never reached the provider.
func transportFailure(err error) *CallError {
	return &CallError{Kind: KindTransport, Err: fmt.Errorf("http request: %w", err)}
}

// upstreamFailureAt is a 200 that carried no usable reply — an error object, or
// no choices at all. The body travels because it is the only evidence.
func upstreamFailureAt(body string, err error) *CallError {
	return &CallError{Kind: KindUpstream, Status: http.StatusOK, Body: body, Err: err}
}

// nothingVisible reports whether a choice carries neither content nor a tool
// call: the call succeeded and produced nothing.
func nothingVisible(c Choice) bool {
	return len(c.Message.ToolCalls) == 0 && strings.TrimSpace(c.Message.Content) == ""
}
