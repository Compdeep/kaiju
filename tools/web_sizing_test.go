package tools

import (
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// How much of a page is read in one pass, and how long a reply may be, are
// properties of the model that will read them. They used to be two numbers in
// this file — 16000 and 1024 — which wasted most of a large model's window and,
// on a long page, answered about the first few pages and said nothing about the
// rest.

func clientWithWindow(ctxTokens, outTokens int) *llm.Client {
	c := llm.NewClientWithProvider("openai", "http://127.0.0.1:1", "unused", "a-model")
	return c.Limits(func(string) (int, int) { return ctxTokens, outTokens })
}

func TestReadingWindow_ComesFromTheModel(t *testing.T) {
	small := &WebFetch{executor: clientWithWindow(8_000, 512), limits: FetchLimits{}.resolve()}
	large := &WebFetch{executor: clientWithWindow(200_000, 8_000), limits: FetchLimits{}.resolve()}

	smallPass, smallReply := small.readingWindow(600)
	largePass, largeReply := large.readingWindow(600)

	if largePass <= smallPass {
		t.Errorf("a model with a bigger window reads %d characters a pass and a smaller one reads %d — the window is not reaching the sizing", largePass, smallPass)
	}
	if smallReply != 512 || largeReply != 8_000 {
		t.Errorf("reply sizes = %d and %d, want the models' own 512 and 8000", smallReply, largeReply)
	}
	// The whole point: a large model reads a long page in one pass.
	if largePass < 200_000 {
		t.Errorf("a 200k-token window yielded only %d characters a pass, which wastes most of it", largePass)
	}
}

// A model that will not say has to be handled, and the answer is what this tool
// did before it could ask — not something new and unpredictable.
func TestReadingWindow_UnknownModelFallsBackToTheOneNumber(t *testing.T) {
	for _, w := range []*WebFetch{
		{executor: nil, limits: FetchLimits{}.resolve()},
		{executor: clientWithWindow(0, 0), limits: FetchLimits{}.resolve()},
	} {
		pass, reply := w.readingWindow(600)
		if pass != unknownModelWindowChars {
			t.Errorf("pass = %d, want the unknown-model number %d", pass, unknownModelWindowChars)
		}
		if reply != unknownModelReplyTokens {
			t.Errorf("reply = %d, want %d", reply, unknownModelReplyTokens)
		}
	}
}

// The budget is in tokens, so what it buys moves with the model instead of
// being a count that is right on one and wrong on the rest.
func TestChunksAffordable_IsABudgetNotACount(t *testing.T) {
	w := &WebFetch{limits: FetchLimits{ExtractTokenBudget: 30_000}.resolve()}

	// Small pieces: the budget pays for many.
	if got := w.chunksAffordable(100, 3_000); got != 30 {
		t.Errorf("with 3000-character pieces the budget bought %d, want 30", got)
	}
	// Large pieces: the same budget pays for few.
	if got := w.chunksAffordable(100, 30_000); got != 3 {
		t.Errorf("with 30000-character pieces the budget bought %d, want 3", got)
	}
	// Never more than the page has.
	if got := w.chunksAffordable(2, 3_000); got != 2 {
		t.Errorf("bought %d pieces of a 2-piece page", got)
	}
	// Never nothing: a budget that pays for no reading is a misconfiguration,
	// and returning an empty result would hide it.
	tiny := &WebFetch{limits: FetchLimits{ExtractTokenBudget: 1}.resolve()}
	if got := tiny.chunksAffordable(50, 30_000); got < 1 {
		t.Errorf("a tiny budget bought %d pieces; it must still read one", got)
	}
}

func TestSplitForReading_CutsOnParagraphsAndKeepsEverything(t *testing.T) {
	para := strings.Repeat("x", 400)
	text := strings.Join([]string{para, para, para, para, para}, "\n\n")

	pieces := splitForReading(text, 900)
	if len(pieces) < 2 {
		t.Fatalf("a %d-character text at 900 a piece came to %d pieces", len(text), len(pieces))
	}
	joined := strings.ReplaceAll(strings.Join(pieces, ""), "\n", "")
	if strings.Count(joined, "x") != strings.Count(text, "x") {
		t.Errorf("splitting lost text: %d characters in, %d out", strings.Count(text, "x"), strings.Count(joined, "x"))
	}
	for i, p := range pieces {
		if len(p) > 900 {
			t.Errorf("piece %d is %d characters, over the %d it was given", i, len(p), 900)
		}
	}
	// A text that already fits is one piece, unchanged.
	if one := splitForReading("short", 900); len(one) != 1 || one[0] != "short" {
		t.Errorf("a short text was split: %v", one)
	}
}
