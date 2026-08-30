package tools

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

// The two pages that cost a run its answer, as they arrived.
//
// Both are the head of a real response, kept because the point of the check is
// that these two are not an ordinary 429 and an ordinary 403. Neither body is
// invented: one came back from explorer.solana.com with HTTP 429, the other from
// etherscan.io with HTTP 403.
const (
	vercelCheckpoint = `<!DOCTYPE html><html lang="en" data-astro-cid-nbv56vs3> <head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1"><meta name="theme-color" content="#000">` +
		`<title>Vercel Security Checkpoint</title><style>.spinner{display:flex}</style>`

	cloudflareInterstitial = `<!DOCTYPE html><html lang="en-US"><head><title>Just a moment...</title>` +
		`<meta http-equiv="Content-Type" content="text/html; charset=UTF-8">` +
		`<meta name="robots" content="noindex,nofollow">`
)

func errResult(t *testing.T, status, body string) toolapi.ToolMessage {
	t.Helper()
	data, err := json.Marshal(fetchResult{Status: status, Content: body})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return toolapi.ToolMessage{Type: "page", Status: toolapi.StatusError, Detail: status, Data: data}
}

func TestChallengeReasonNamesTheWall(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"vercel checkpoint", vercelCheckpoint, true},
		{"cloudflare interstitial", cloudflareInterstitial, true},
		{"an ordinary 404 page", `<html><head><title>Not Found</title></head><body>No such block.</body></html>`, false},
		{"an API saying no", `{"success":false,"errors":{"code":401,"message":"Token is missing or invalid"}}`, false},
		{"empty body", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := challengeReason(c.body) != ""
			if got != c.want {
				t.Errorf("challengeReason found a wall = %v, want %v", got, c.want)
			}
		})
	}
}

// A marker deep in a long document is a page discussing the thing, not the thing.
func TestChallengeReasonReadsOnlyTheHead(t *testing.T) {
	buried := make([]byte, 9000)
	for i := range buried {
		buried[i] = 'x'
	}
	if challengeReason(string(buried)+"Just a moment...") != "" {
		t.Error("a marker 9KB into the body was treated as a challenge")
	}
}

func TestWebFetchJudgesItsOwnFailure(t *testing.T) {
	w := NewWebFetch()

	cases := []struct {
		name    string
		msg     toolapi.ToolMessage
		want    toolapi.RetryVerdict
		andWait time.Duration
	}{
		{
			name: "429 that is really a bot challenge",
			msg:  errResult(t, "HTTP 429 429 Too Many Requests", vercelCheckpoint),
			want: toolapi.RetryNever,
		},
		{
			name: "403 that is really a bot challenge",
			msg:  errResult(t, "HTTP 403 403 Forbidden", cloudflareInterstitial),
			want: toolapi.RetryNever,
		},
		{
			name:    "429 that is really a rate limit",
			msg:     errResult(t, "HTTP 429 429 Too Many Requests", `{"error":"slow down"}`),
			want:    toolapi.RetryAfter,
			andWait: 5 * time.Second,
		},
		{
			name:    "the host fell over",
			msg:     errResult(t, "HTTP 503 503 Service Unavailable", "upstream is down"),
			want:    toolapi.RetryAfter,
			andWait: 5 * time.Second,
		},
		{
			name: "a missing token is settled",
			msg:  errResult(t, "HTTP 401 401 Unauthorized", `{"errors":{"code":401}}`),
			want: toolapi.RetryNever,
		},
		{
			name: "a page that is not there is settled",
			msg:  errResult(t, "HTTP 404 404 Not Found", "no such page"),
			want: toolapi.RetryNever,
		},
		{
			name: "a status this tool has no view on",
			msg:  errResult(t, "HTTP 418 418 I'm a teapot", "short and stout"),
			want: toolapi.RetryUnknown,
		},
		{
			name: "a result that did not fail",
			msg:  toolapi.ToolOK("page", "the page", nil),
			want: toolapi.RetryUnknown,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := w.RetryAdvice(c.msg)
			if got.Verdict != c.want {
				t.Fatalf("verdict = %v, want %v (why: %q)", got.Verdict, c.want, got.Why)
			}
			if c.andWait != 0 && got.Wait != c.andWait {
				t.Errorf("wait = %s, want %s", got.Wait, c.andWait)
			}
			if got.Verdict != toolapi.RetryUnknown && got.Why == "" {
				t.Error("a verdict with no reason — the reflector plans from the reason")
			}
		})
	}
}

// The engine falls back to its own classification for anything that has no view,
// which is every tool that does not implement the interface.
func TestAToolWithNoOpinionSaysSo(t *testing.T) {
	if got := toolapi.ToolRetryAdvice(NewWebSearch(), errResult(t, "HTTP 429", "")); got.Verdict != toolapi.RetryUnknown {
		t.Errorf("web_search verdict = %v, want RetryUnknown", got.Verdict)
	}
}
