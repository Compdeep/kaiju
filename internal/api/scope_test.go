package api

import (
	"testing"

	"github.com/Compdeep/kaiju/internal/auth"
)

// The security contract of the tool-scope fix: an authenticated request may reach
// ONLY the tools its signed token grants, and a request that grants nothing fails
// CLOSED (no tools) — never falling open to the full registry. Only a token-less
// local caller (nil claims) gets unrestricted access.
func TestResolveToolScope(t *testing.T) {
	// 1. Token-less (local CLI): nil scope means "full access" downstream.
	if got := resolveToolScope(nil, nil); got != nil {
		t.Fatalf("nil claims must yield nil scope (unrestricted CLI), got %+v", got)
	}

	// 2. Explicit grant: exactly those tools, nothing else.
	s := resolveToolScope(&auth.Claims{Username: "org1:u1", Tools: []string{"web_search", "web_fetch"}}, nil)
	if s == nil {
		t.Fatal("granted claims must yield a scope, got nil")
	}
	if !s.AllowedTools["web_search"] || !s.AllowedTools["web_fetch"] {
		t.Fatalf("granted tools missing from scope: %+v", s.AllowedTools)
	}
	for _, forbidden := range []string{"bash", "process_kill", "file_read", "env_list", "*"} {
		if s.AllowedTools[forbidden] {
			t.Fatalf("scope leaked %q — grant must be exact", forbidden)
		}
	}

	// 3. Authenticated but NOTHING granted (no Tools claim, no DB user): the
	//    critical case — must be an EMPTY scope (deny), not nil (which would be
	//    full access). This is the bug the fix closes.
	s = resolveToolScope(&auth.Claims{Username: "org1:u1"}, nil)
	if s == nil {
		t.Fatal("authenticated no-grant must be an empty scope, got nil (would be full access)")
	}
	if len(s.AllowedTools) != 0 {
		t.Fatalf("no-grant must allow zero tools, got %+v", s.AllowedTools)
	}
	for _, forbidden := range []string{"bash", "web_search", "*"} {
		if s.AllowedTools[forbidden] {
			t.Fatalf("no-grant scope allowed %q — must fail closed", forbidden)
		}
	}

	// 4. Wildcard grant: everything (for an admin/trusted host token).
	s = resolveToolScope(&auth.Claims{Username: "admin", Tools: []string{"*"}}, nil)
	if s == nil || !s.AllowedTools["*"] {
		t.Fatalf("wildcard grant must set the * allow-all, got %+v", s)
	}
}

// The ceiling rides with the scope. A token that grants tools also says how far
// a run may go; a scope that left that at zero would, now that the engine lowers
// a pinned intent to the scope's ceiling, hold every token-based caller to
// observe. A request granted nothing keeps a ceiling of zero: it fails closed on
// tools and on intent alike.
func TestResolveToolScope_CarriesTheTokensCeiling(t *testing.T) {
	s := resolveToolScope(&auth.Claims{Username: "org1:u1", MaxIntent: 100, Tools: []string{"web_search"}}, nil)
	if s == nil || s.MaxIntent != 100 {
		t.Fatalf("a Tools-claim scope must carry the token's ceiling of 100, got %+v", s)
	}
	s = resolveToolScope(&auth.Claims{Username: "org1:u1", MaxIntent: 200}, nil)
	if s == nil || s.MaxIntent != 0 || len(s.AllowedTools) != 0 {
		t.Fatalf("a no-grant scope must stay at zero tools and zero ceiling whatever the token claims, got %+v", s)
	}
}
