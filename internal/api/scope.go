package api

import (
	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/internal/auth"
	"github.com/Compdeep/kaiju/internal/db"
)

// resolveToolScope derives the DENY-BY-DEFAULT tool scope for a request from its
// already-verified JWT claims. It is the security boundary for tool access, and
// is deliberately a small pure function so it can be tested directly:
//
//   - nil claims (a token-less local CLI) ⇒ nil scope ⇒ full access (trusted local).
//   - an explicit Tools claim (stamped by the host) ⇒ EXACTLY those
//     tools; "*" means everything. This is the authority — the caller never sees
//     the signed token, so it cannot widen the grant.
//   - else a provisioned kaiju user's DB scope (e.g. the local admin).
//   - else — authenticated but nothing granted — an EMPTY scope: no tools. An
//     empty AllowedTools makes relevantTools return [] (the planner sees nothing)
//     and the execution gate reject every call, so the request fails CLOSED,
//     never falling open to the full registry.
func resolveToolScope(claims *auth.Claims, database *db.DB) *agent.ResolvedScope {
	if claims == nil {
		return nil // token-less local operator → unrestricted
	}
	if len(claims.Tools) > 0 {
		allowed := make(map[string]bool, len(claims.Tools))
		for _, t := range claims.Tools {
			allowed[t] = true // "*" here still means "everything"
		}
		// The token's own ceiling rides with the tools it grants. It used to be
		// left at zero, which read as "unstated" only because nothing in the
		// engine compared a PINNED intent against a scope's ceiling. Now that
		// Trigger.Intent lowers a pin to the ceiling it is handed, a zero here
		// would hold every token-based caller to observe. The host stamped
		// MaxIntent into the claims for exactly this purpose, and the execute
		// handler already lowers a requested intent by it; carrying it on the
		// scope says the same thing in the one place the engine reads.
		return &agent.ResolvedScope{Username: claims.Username, AllowedTools: allowed, MaxIntent: claims.MaxIntent}
	}
	if database != nil {
		if user, err := database.GetUser(claims.Username); err == nil {
			if dbScope, err := database.ResolveUserScope(user); err == nil {
				return &agent.ResolvedScope{
					Username:     dbScope.Username,
					AllowedTools: dbScope.AllowedTools,
					MaxImpact:    dbScope.MaxImpact,
					MaxIntent:    dbScope.MaxIntent,
				}
			}
		}
	}
	return &agent.ResolvedScope{Username: claims.Username, AllowedTools: map[string]bool{}}
}
