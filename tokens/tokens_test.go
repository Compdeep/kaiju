package tokens

import (
	"context"
	"testing"
)

func TestAdd_AttributesByPrincipalAndCategory(t *testing.T) {
	ctx := WithPrincipal(WithCategory(context.Background(), "cat-x"), "prin-x")
	_, before := Snapshot()

	Add(ctx, 10)
	Add(ctx, 5)
	Add(ctx, 0)  // no-op
	Add(ctx, -3) // no-op

	usage, total := Snapshot()
	if total-before != 15 {
		t.Fatalf("total delta = %d, want 15", total-before)
	}
	var got int64
	for _, u := range usage {
		if u.Principal == "prin-x" && u.Category == "cat-x" {
			got = u.Tokens
		}
	}
	if got != 15 {
		t.Fatalf("prin-x/cat-x tokens = %d, want 15", got)
	}
}

func TestCategoryFrom_DefaultsToOther(t *testing.T) {
	if got := CategoryFrom(context.Background()); got != "other" {
		t.Fatalf("default category = %q, want %q", got, "other")
	}
}
