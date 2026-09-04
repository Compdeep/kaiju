package llm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

/*
 * Stop paying for a provider that is not answering.
 *
 * An upstream failure is billed for everything the model generated before it
 * was abandoned, returns nothing, and the caller's own retry sends the same
 * work again. Measured on one deployment: a provider answering HTTP 200 with
 * {"error":{"message":"The operation was aborted","code":504}} on 46 of 57
 * calls in three hours, 1.15 million tokens of which about 40% was paid for and
 * thrown away.
 *
 * Only the provider's own failures count. A truncated reply, an answer that
 * will not parse, a model that ignored the schema — those are answers, and a
 * run of bad ones must not stop every caller from asking.
 */

// ErrProviderUnavailable is returned instead of making a request while the
// breaker is open. Callers can tell it from a failure of their own request:
// nothing was sent, so nothing was billed and nothing about their prompt is
// implicated.
var ErrProviderUnavailable = errors.New("provider unavailable: too many consecutive upstream failures")

const (
	// breakerThreshold is how many consecutive upstream failures open it.
	//
	// Consecutive rather than a rate: one success is enough to say the provider
	// is answering, and a threshold counted globally across callers sees an
	// outage sooner than any one of them could.
	breakerThreshold = 10

	// breakerCooldown is how long it stays open. One request is allowed through
	// after it, and that request's outcome decides whether it closes or the
	// cooldown starts again — so a provider still down costs one call per
	// cooldown rather than one per caller.
	breakerCooldown = 5 * time.Minute
)

type breaker struct {
	mu          sync.Mutex
	consecutive int
	openedAt    time.Time
	open        bool
	lastReason  string
}

var providerBreaker = &breaker{}

/*
 * allow reports whether a request may be sent.
 *
 * A breaker past its cooldown closes here rather than on the next success, so
 * exactly one request is in flight to decide it. Closing on success instead
 * would let every waiting caller through at once, which is the stampede this
 * exists to prevent.
 */
func (b *breaker) allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return nil
	}
	if time.Since(b.openedAt) < breakerCooldown {
		return fmt.Errorf("%w (%d consecutive: %s)", ErrProviderUnavailable, b.consecutive, b.lastReason)
	}
	b.open = false
	b.consecutive = breakerThreshold - 1 // one more failure reopens it
	log.Printf("[llm] provider breaker: trying one request after %s", breakerCooldown)
	return nil
}

// succeeded records a reply that arrived. Any answer closes it: the provider is
// answering, whatever the model said.
func (b *breaker) succeeded() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.consecutive > 0 || b.open {
		log.Printf("[llm] provider breaker: reset after a successful call")
	}
	b.consecutive, b.open = 0, false
}

// failed records an upstream failure and opens the breaker at the threshold.
func (b *breaker) failed(reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consecutive++
	b.lastReason = reason
	if b.consecutive >= breakerThreshold && !b.open {
		b.open, b.openedAt = true, time.Now()
		log.Printf("[llm] provider breaker OPEN after %d consecutive upstream failures (%s) — "+
			"no requests for %s", b.consecutive, reason, breakerCooldown)
	}
}

/*
 * upstreamFailure reports whether an error is the provider's rather than an
 * answer we did not like, and names it for the log.
 *
 * The distinction is the whole safety of this: a reply that ran out of room or
 * would not parse is a reply, and counting those would let a model having a bad
 * run stop every caller from asking anything.
 *
 * param: err - what the request returned.
 * return: whether it was upstream, and a short reason.
 */
func upstreamFailure(err error) (bool, string) {
	if err == nil {
		return false, ""
	}
	// Our own cancellation is not the provider's fault.
	if errors.Is(err, context.Canceled) {
		return false, ""
	}
	// A reply that arrived and was not what we wanted.
	if errors.Is(err, ErrReplyTruncated) {
		return false, ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true, "timed out"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "provider returned an error with HTTP 200"),
		strings.Contains(msg, "provider returned no choices"):
		return true, "200 with no reply"
	case strings.Contains(msg, "context deadline exceeded"):
		return true, "timed out"
	case strings.Contains(msg, fmt.Sprintf("HTTP %d", http.StatusTooManyRequests)):
		return true, "rate limited"
	}
	// Any 5xx: the request never reached a working model.
	for code := 500; code <= 599; code++ {
		if strings.Contains(msg, fmt.Sprintf("HTTP %d", code)) {
			return true, fmt.Sprintf("HTTP %d", code)
		}
	}
	return false, ""
}
