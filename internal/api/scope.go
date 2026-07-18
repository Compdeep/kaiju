package api

import (
	"github.com/Compdeep/kaiju/internal/agent"
	"github.com/Compdeep/kaiju/internal/auth"
	"github.com/Compdeep/kaiju/internal/db"
)

// resolveToolScope derives the DENY-BY-DEFAULT tool scope for a request from its
// already-verified JWT claims. It is the security boundary for tool access, and
// is deliberately a small pure function so it can be tested directly:
//
//   - nil claims (a token-less local CLI) ⇒ nil scope ⇒ full access (trusted local).
//   - an explicit Tools claim (stamped by the host, e.g. makeen) ⇒ EXACTLY those
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
		return &agent.ResolvedScope{Username: claims.Username, AllowedTools: allowed}
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
