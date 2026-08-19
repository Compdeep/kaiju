package ui

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/agent/uploads"
	"github.com/Compdeep/kaiju/internal/api"
	"github.com/Compdeep/kaiju/internal/clearance"
	"github.com/Compdeep/kaiju/internal/db"
	"github.com/Compdeep/kaiju/internal/gateway"
)

/*
 * Options is everything Handler is given.
 * desc: One struct rather than a list of parameters, so a later addition does
 *       not break every application that already calls Handler.
 */
type Options struct {
	// Agent answers the conversations. Required; there is no interface without
	// one.
	Agent *agent.Agent

	// Store is history and accounts. Nil means neither — see Store.
	Store Store

	// Auth is the sign-in. Nil means none, and requires Store to be nil too —
	// see Authenticator.
	Auth Authenticator

	// Config is the brand, the theme and which sections exist. The zero value
	// is kaiju's own colours under kaiju's own name with every optional section
	// off.
	Config Config

	// DefaultIntent is the rank a request runs at when it names none. It is
	// capped by whatever the token and the scope allow, so it is a starting
	// point rather than a grant.
	DefaultIntent int

	// SetToolState is where the application keeps a tool state changed from the
	// interface. It is given the tool's name and its new state, which is "off"
	// or "local" and never "everywhere" — see tools.go for why that one is not
	// granted or withdrawn from a machine's own page.
	//
	// Nil means the panel is read-only: it shows each tool and its state, and
	// offers no way to change one. That is the safe default, because a writable
	// panel decides what this machine's agent may do and an application that
	// has not thought about who reaches it should not get one by omission.
	SetToolState func(name, state string) error
}

/*
 * Handler returns the interface and the routes it calls, mounted and ready to
 * serve.
 * desc: One call replaces the wiring an application would otherwise copy: the
 *       static page, the configuration it reads before it draws, the execution
 *       and conversation routes, the event stream, and — where their section is
 *       on — the workspace and the administration surface. What is registered
 *       depends on what it was given: no Store and the routes that would read
 *       one are absent rather than answering emptily, no Auth and nothing is
 *       wrapped in a token check.
 *
 *       Two things a caller cannot infer. It installs a clearance checker on
 *       the agent, because the endpoints it mounts are the only way to change
 *       one and a checker nothing reads would make those endpoints silent. And
 *       it does not mount kaiju's own documentation, its health check or its
 *       configuration editor: those belong to kaiju's daemon, not to an
 *       interface an application embeds.
 *
 *       The returned handler is a mux. Mount it at the root of whatever server
 *       is serving, or under a prefix if the paths are stripped first.
 * param: opts - see Options.
 * return: the mounted handler, or an error if the options do not describe an
 *         interface that can work.
 */
func Handler(opts Options) (http.Handler, error) {
	if opts.Agent == nil {
		return nil, errors.New("ui: no agent — there is nothing for the interface to talk to")
	}
	if opts.Auth != nil && opts.Store == nil {
		return nil, errors.New("ui: an authenticator with no store — the accounts live in the store, so nobody could ever sign in")
	}
	if err := opts.Config.Validate(); err != nil {
		return nil, err
	}

	mux := http.NewServeMux()

	// How a route is protected. With no Authenticator both are the identity
	// wrapper, so every mount below reads the same whether or not there is a
	// sign-in.
	// With no Authenticator there is no token to check and nothing to protect
	// with, so what stands in its place is a refusal of anything a browser says
	// came from another site — see samesite.go for why that is the boundary.
	protect, protectQuery := sameSiteOnly, sameSiteOnly
	if opts.Auth != nil {
		svc := opts.Auth.jwt()
		protect = func(h http.Handler) http.Handler { return gateway.WithJWTAuth(svc)(h) }
		// Browsers cannot set a header on an EventSource or an <img>, so these
		// take the token from the query string instead.
		protectQuery = func(h http.Handler) http.Handler { return gateway.WithJWTAuthOrQuery(svc)(h) }
	}

	database := databaseOf(opts.Store)

	// What the page reads before it draws anything. Outside the token check:
	// the sign-in page carries the brand and is drawn before anyone has one.
	cfgHandler, err := ConfigHandler(opts.Config)
	if err != nil {
		return nil, err
	}
	mux.Handle(ConfigPath, cfgHandler)

	// Sign-in. Only where there is somewhere to look an account up.
	if opts.Auth != nil {
		authMux := http.NewServeMux()
		api.NewAuthAPI(database, opts.Auth.jwt()).RegisterRoutes(authMux)
		mux.Handle("/api/v1/auth/login", authMux) // signing in cannot require being signed in
		mux.Handle("/api/v1/auth/me", protect(authMux))
	}

	// The clearance checker, and the endpoints that change it. Built here
	// because the routes below are the only way to add one, and installed on
	// the agent because the gate is what reads it.
	checker := clearance.NewChecker()
	if database != nil {
		if endpoints, lerr := database.ListClearanceEndpoints(); lerr == nil {
			for _, ep := range endpoints {
				checker.SetEndpoint(clearance.Endpoint{
					ToolName: ep.ToolName, URL: ep.URL,
					TimeoutMs: ep.TimeoutMs, Headers: ep.Headers,
				})
			}
			if len(endpoints) > 0 {
				log.Printf("[ui] loaded %d clearance endpoints", len(endpoints))
			}
		}
	}
	opts.Agent.SetClearanceChecker(checker)

	execAPI := api.New(opts.Agent, opts.DefaultIntent, database, opts.Agent.LLMClient(), checker)
	execAPI.SetUploadProcessor(uploads.New(opts.Agent, opts.Agent.ExecutorClient()))
	execMux := http.NewServeMux()
	execAPI.RegisterRoutes(execMux)

	// Running a query, and the controls over one in flight. These need no
	// store: every use of one on this path is already conditional on there
	// being one, so a conversation without a store simply keeps nothing.
	// Changing a tool's state, where the application says where to keep it.
	// Registered only then: with nowhere to keep a change, a panel that offered
	// one would silently forget it on the next restart.
	if opts.SetToolState != nil {
		mux.Handle("/api/v1/tools/", protect(toolStateHandler(opts.Agent, opts.SetToolState)))
	}

	for _, p := range []string{
		"/api/v1/execute",
		"/api/v1/oneshot",
		"/api/v1/interject",
		"/api/v1/stop",
		"/api/v1/tools",
		"/api/v1/status",
		"/api/v1/usage",
	} {
		mux.Handle(p, protect(execMux))
	}

	// Conversations, what was said in them, and what was remembered from them.
	// Absent without a store, rather than present and empty.
	if database != nil {
		for _, p := range []string{
			"/api/v1/sessions", "/api/v1/sessions/",
			"/api/v1/memories", "/api/v1/memories/",
			"/api/v1/clearance", "/api/v1/clearance/",
		} {
			mux.Handle(p, protect(execMux))
		}
	}

	// The live trace. Which handler depends on whether callers can be told
	// apart: with a sign-in the stream is filtered to the sessions its caller
	// owns, and without one there is a single caller and nothing to filter.
	if opts.Auth != nil {
		mux.Handle("/events", protectQuery(gateway.SSEHandler(opts.Agent, database)))
	} else {
		mux.Handle("/events", sameSiteOnly(gateway.SSEHandlerSinglePrincipal(opts.Agent)))
	}

	if opts.Config.Sections.Workspace {
		mux.Handle("/api/v1/workspace/files", protect(execMux))
		// Serving a file takes its token from the query, because a browser
		// cannot put a header on an <img>, a <video> or an iframe.
		mux.Handle("/api/v1/workspace/serve", protectQuery(execMux))
		// A live preview's own sub-resources — its scripts, styles and images —
		// carry no token either, and there is nowhere to put one. The page that
		// asked for it has already been through the check above.
		if opts.Auth != nil {
			mux.Handle("/api/v1/workspace/live/", execMux)
		} else {
			mux.Handle("/api/v1/workspace/live/", sameSiteOnly(execMux))
		}
		mux.Handle("/api/v1/workspace/write", protect(execMux))
	}

	if err := mountAdministration(mux, opts, database, protect); err != nil {
		return nil, err
	}

	// The page itself, last: it is the fallback for everything above.
	mux.Handle("/", gateway.WebUIHandler())

	// Wrapped once, around everything, so a route added later cannot be added
	// without them.
	return gateway.WithSecurityHeaders(mux), nil
}

/*
 * mountAdministration registers the users section, or the one read that
 * outlives it.
 * desc: Separate from Handler because the exception in it needs explaining and
 *       Handler is long enough. With the section on, everything is mounted.
 *       With it off, the intent list alone stays readable: the input bar names
 *       the ranks from it on every load, so dropping it would empty that
 *       control in an interface whose conversation is all that is left. The
 *       writes on the same path go with the section.
 * param: mux - where to mount.
 * param: opts - the options Handler was given.
 * param: database - the store's database, or nil.
 * param: protect - the token wrapper.
 * return: an error if the section was asked for and cannot be served.
 */
func mountAdministration(mux *http.ServeMux, opts Options, database *db.DB, protect func(http.Handler) http.Handler) error {
	if database == nil {
		if opts.Config.Sections.Users {
			return fmt.Errorf("ui: the users section was asked for with no store — users, groups and scopes all live in one")
		}
		return nil
	}

	mgmtMux := http.NewServeMux()
	api.NewScopeAPI(database).RegisterRoutes(mgmtMux)
	api.NewGroupAPI(database).RegisterRoutes(mgmtMux)
	api.NewUserAPI(database).RegisterRoutes(mgmtMux)
	api.NewIntentAPI(database, opts.Agent).RegisterRoutes(mgmtMux)

	if !opts.Config.Sections.Users {
		mux.Handle("/api/v1/intents", protect(readOnly(mgmtMux)))
		return nil
	}

	for _, p := range []string{
		"/api/v1/scopes", "/api/v1/scopes/",
		"/api/v1/groups", "/api/v1/groups/",
		"/api/v1/users", "/api/v1/users/",
		"/api/v1/intents", "/api/v1/intents/",
		"/api/v1/tool-intents", "/api/v1/tool-intents/",
	} {
		mux.Handle(p, protect(mgmtMux))
	}
	return nil
}

/*
 * readOnly passes GET through and refuses every other method.
 * desc: Used where one route of a switched-off section stays reachable because
 *       something outside that section reads it. It separates by method, not by
 *       path, because the handler behind that path registers several verbs on
 *       one pattern and there is nowhere else to draw the line.
 * param: h - the handler to protect.
 * return: a handler that answers GET and refuses the rest.
 */
func readOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.ServeHTTP(w, r)
	})
}
