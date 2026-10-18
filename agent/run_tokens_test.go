package agent

import (
	"context"
	"testing"

	"github.com/Compdeep/kaiju/agent/llm"
)

// A run's account sums every reply it is shown, with the thinking split out,
// and prints as fixed pairs for the run-complete line.
func TestRunTokens_SumsTheRepliesItIsShown(t *testing.T) {
	acct := &runTokens{}
	ctx := withRunTokens(context.Background(), acct)

	planner := llm.Usage{PromptTokens: 20213, CompletionTokens: 1189}
	verdict := llm.Usage{PromptTokens: 12554, CompletionTokens: 1306, Details: &llm.UsageDetails{ReasoningTokens: 861}}
	runTokensFrom(ctx).add(planner)
	runTokensFrom(ctx).add(verdict)

	if got, want := acct.String(), "calls=2 in=32767 out=2495 think=861"; got != want {
		t.Errorf("account = %q, want %q", got, want)
	}
}

// Outside a run there is no account. Adding to none is allowed and counts
// nothing, and the line still has its shape.
func TestRunTokens_NoneOutsideARun(t *testing.T) {
	var none *runTokens
	none.add(llm.Usage{PromptTokens: 5})
	if got := runTokensFrom(context.Background()); got != nil {
		t.Errorf("a bare context carried an account: %v", got)
	}
	if got, want := none.String(), "calls=0 in=0 out=0 think=0"; got != want {
		t.Errorf("nil account printed %q, want %q", got, want)
	}
	if ctx := withRunTokens(context.Background(), nil); runTokensFrom(ctx) != nil {
		t.Error("a nil account was stamped on the context")
	}
}
