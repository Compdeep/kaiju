// Package permissions works out what tools a person may use.
//
// An application decides who somebody is; this decides what they may do once it knows.
// The answer is a Grant, which a caller turns into whatever its own code expects — for
// the agent in this module, an agent.ResolvedScope on a Trigger.
//
// It exists because the same code was written twice — once here and once in an
// application built on this package — and the two copies came to disagree. One read a
// user's groups and the other did not, so the same person was allowed different tools
// depending on which program asked. Neither program could see the other's copy: Go
// forbids reading another module's internal packages, so no compiler and no test could
// compare them, and the difference sat there unnoticed.
//
// Nothing here touches a database. The caller looks up the user, the permission sets and
// the groups, and hands over the values. An application keeping its people in a
// directory service, in a table of its own, or in a file gets the same answer as one
// using SQLite, and this package needs to know about none of them.
package permissions

// Grant is what a person may do: which tools they may call, how far each of those may
// go, and how far any run of theirs may go.
//
// It carries the same four things as agent.ResolvedScope, and is a separate type on
// purpose: this package depends on nothing, so an application can work out permissions
// without pulling in the agent, and the agent can be tested without pulling in this. A
// caller converts, which is four lines and what both existing callers already do.
type Grant struct {
	Username string
	// AllowedTools holds the tools by name. The entry "*" means every tool.
	AllowedTools map[string]bool
	// MaxImpact is a ceiling for particular tools, by name. A tool absent from here
	// is limited only by the ceilings that apply to everything.
	MaxImpact map[string]int
	// MaxIntent is how far any run of theirs may go.
	MaxIntent int
}

// User is one person and what has been granted to them.
//
// Scopes and Groups hold names, not the things themselves; Resolve is given those
// separately. A name with nothing behind it is ignored rather than treated as an error —
// a permission set deleted while a user still refers to it grants nothing, which is the
// safe reading and what both existing copies already do.
type User struct {
	Username string
	// Scopes are granted to this person directly.
	Scopes []string
	// Groups they belong to. Every scope those groups hold is granted as well.
	Groups []string
	// MaxIntent is the highest this person may ever reach, whatever their scopes
	// allow. The final answer is never above it.
	MaxIntent int
}

// Scope is one named set of permissions.
type Scope struct {
	Name string
	// Tools this scope allows by name. The single entry "*" means every tool.
	Tools []string
	// Cap is a ceiling for particular tools, by name. A tool absent from here is
	// limited only by the ceilings that apply to everything.
	Cap map[string]int
	// IntentCap is the highest a person holding this scope may reach through it.
	IntentCap int
}

// Group is a named bundle of scopes. A group holds scope names and never other groups,
// so there is no nesting to walk and no cycle to guard against.
type Group struct {
	Name   string
	Scopes []string
}

/*
 * Resolve returns what a user may do.
 *
 * Their scopes are their own plus every scope held by every group they belong to. Across
 * that set: the allowed tools are added together, the ceiling for a particular tool is
 * the lowest any scope sets, and the ceiling on how far a run may go is the highest any
 * scope allows — then lowered to the user's own if that is lower.
 *
 * Adding up the tools while taking the lowest per-tool ceiling is deliberate and is how
 * both existing copies behave: holding two scopes widens which tools are reachable and
 * never raises how far any one of them may go.
 *
 * A user with no scopes and no groups is allowed nothing. So is one whose every scope
 * and group name has nothing behind it. Neither is an error.
 *
 * Resolving the same scope twice — granted directly and through a group, or through two
 * groups — changes nothing, since adding a tool already allowed, taking the lower of two
 * equal ceilings and the higher of two equal ones all leave the answer as it was.
 *
 * param: u - the person.
 * param: scopes - every scope the application knows, by name.
 * param: groups - every group the application knows, by name.
 * return: what they may do. Never fails: values in, value out.
 */
func Resolve(u User, scopes map[string]Scope, groups map[string]Group) Grant {
	answer := Grant{
		Username:     u.Username,
		AllowedTools: map[string]bool{},
		MaxImpact:    map[string]int{},
	}

	// Their own scopes, then every scope their groups hold. A group name with nothing
	// behind it contributes nothing, exactly as an unknown scope name does.
	names := make([]string, 0, len(u.Scopes)+len(u.Groups))
	names = append(names, u.Scopes...)
	for _, groupName := range u.Groups {
		if g, known := groups[groupName]; known {
			names = append(names, g.Scopes...)
		}
	}

	// Highest ceiling any of their scopes allows. Starts at nothing, so a user whose
	// scopes are all unknown is allowed nothing rather than everything.
	reachable := 0

	for _, name := range names {
		scope, known := scopes[name]
		if !known {
			continue
		}
		for _, tool := range scope.Tools {
			answer.AllowedTools[tool] = true
		}
		for tool, cap := range scope.Cap {
			if existing, seen := answer.MaxImpact[tool]; !seen || cap < existing {
				answer.MaxImpact[tool] = cap
			}
		}
		if scope.IntentCap > reachable {
			reachable = scope.IntentCap
		}
	}

	answer.MaxIntent = reachable
	if u.MaxIntent < answer.MaxIntent {
		answer.MaxIntent = u.MaxIntent
	}
	return answer
}
