package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent"
	"github.com/Compdeep/kaiju/internal/db"
)

// testAgent builds an agent that never reaches a model. Handler only asks it
// for its clients and installs a clearance checker on it, so nothing here runs
// a query.
func testAgent(t *testing.T) *agent.Agent {
	t.Helper()
	a, err := agent.New(agent.Config{
		ModelConfig: agent.ModelConfig{LLMEndpoint: "http://127.0.0.1:1", LLMAPIKey: "unused", LLMModel: "none"},
		PathConfig:  agent.PathConfig{DataDir: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return a
}

// status reports what the mounted handler answers for a path, which is how a
// route's presence is told from its absence: 404 means nothing is registered
// there, anything else means something is.
func status(t *testing.T, h http.Handler, method, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code
}

func TestHandler_RefusesOptionsThatCannotWork(t *testing.T) {
	if _, err := Handler(Options{}); err == nil {
		t.Error("no agent was accepted")
	}

	a := testAgent(t)
	auth, err := NewAuthenticator("secret", t.TempDir(), 1)
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	if _, err := Handler(Options{Agent: a, Auth: auth}); err == nil {
		t.Error("an authenticator with no store was accepted; nobody could ever sign in")
	}

	if _, err := Handler(Options{Agent: a, Config: Config{Sections: Sections{Users: true}}}); err == nil {
		t.Error("the users section was accepted with no store to keep users in")
	}

	bad := Config{Theme: Theme{Light: map[string]string{"--x": "a;}b{"}}}
	if _, err := Handler(Options{Agent: a, Config: bad}); err == nil {
		t.Error("a theme Validate rejects was accepted")
	}
}

// The shape an application embedding the interface gets when it supplies
// nothing but an agent: a conversation, and no account, history or
// administration behind it.
func TestHandler_NoStoreNoAuth_IsAConversationAndNothingElse(t *testing.T) {
	h, err := Handler(Options{Agent: testAgent(t)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// Present. Not 404, and not 401 either — with no authenticator nothing is
	// behind a token.
	for _, path := range []string{"/api/v1/tools", "/api/v1/status", "/api/v1/usage"} {
		if code := status(t, h, http.MethodGet, path); code == http.StatusNotFound {
			t.Errorf("%s is not registered; the conversation needs it", path)
		} else if code == http.StatusUnauthorized {
			t.Errorf("%s asked for a token when there is no sign-in", path)
		}
	}

	// The page and its configuration.
	if code := status(t, h, http.MethodGet, ConfigPath); code != http.StatusOK {
		t.Errorf("%s = %d, want 200", ConfigPath, code)
	}

	// The live trace, which must not demand a token that cannot exist.
	//
	// Its own request context is cancelled first: the handler streams until the
	// caller goes away, so a request that can never be cancelled never returns.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx))
	if rec.Code == http.StatusUnauthorized {
		t.Error("/events refused a caller for having no token, in a mount that issues none")
	}
	if rec.Code == http.StatusNotFound {
		t.Error("/events is not registered")
	}

	// Absent, rather than present and empty.
	for _, path := range []string{
		"/api/v1/sessions",
		"/api/v1/memories",
		"/api/v1/users",
		"/api/v1/scopes",
		"/api/v1/groups",
		"/api/v1/tool-intents",
		"/api/v1/auth/login",
		"/api/v1/workspace/files",
	} {
		if code := status(t, h, http.MethodGet, path); code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404: there is nothing behind it", path, code)
		}
	}
}

func TestHandler_WorkspaceSectionDecidesItsRoutes(t *testing.T) {
	on, err := Handler(Options{Agent: testAgent(t), Config: Config{Sections: Sections{Workspace: true}}})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if code := status(t, on, http.MethodGet, "/api/v1/workspace/files"); code == http.StatusNotFound {
		t.Error("the workspace section is on and its route is not registered")
	}

	off, err := Handler(Options{Agent: testAgent(t)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if code := status(t, off, http.MethodGet, "/api/v1/workspace/files"); code != http.StatusNotFound {
		t.Errorf("the workspace section is off and its route answered %d", code)
	}
}

// The one read that outlives its section: the input bar names the intent ranks
// from it on every load.
func TestReadOnly_PassesGETAndRefusesTheWrites(t *testing.T) {
	reached := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	h := readOnly(inner)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/intents", nil))
	if rec.Code != http.StatusOK || !reached {
		t.Fatalf("GET did not reach the handler: status=%d reached=%v", rec.Code, reached)
	}

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		reached = false
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/api/v1/intents", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d, want 405", m, rec.Code)
		}
		if reached {
			t.Errorf("%s reached the handler behind a switched-off section", m)
		}
	}
}

func TestStoreOf_NilDatabaseIsNilStore(t *testing.T) {
	if StoreOf(nil) != nil {
		t.Error("a nil database produced a store that is not nil, so every nil check downstream is wrong")
	}
}

// A mount with no sign-in has no token to be missing, so what keeps somebody
// else's page from starting a run is this.
func TestHandler_NoAuth_RefusesACrossSiteRequest(t *testing.T) {
	h, err := Handler(Options{Agent: testAgent(t)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"a page on another site", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"a sibling name", map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"an older browser, by Origin", map[string]string{"Origin": "http://evil.example"}, http.StatusForbidden},
		{"the interface itself", map[string]string{"Sec-Fetch-Site": "same-origin"}, 0},
		{"an address typed in", map[string]string{"Sec-Fetch-Site": "none"}, 0},
		{"an older browser, same origin", map[string]string{"Origin": "http://127.0.0.1:7779"}, 0},
		{"not a browser at all", map[string]string{}, 0},
	}

	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
		req.Host = "127.0.0.1:7779"
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if c.want != 0 && rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d", c.name, rec.Code, c.want)
		}
		if c.want == 0 && rec.Code == http.StatusForbidden {
			t.Errorf("%s: refused, and it should not have been", c.name)
		}
	}
}

// With a sign-in the guard is not installed: a token is attached by hand and is
// never carried along with somebody else's request, so refusing by origin would
// only break a browser on another origin that holds one legitimately.
func TestHandler_WithAuth_AnswersWithTheTokenCheckNotTheOriginGuard(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "kaiju.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	auth, err := NewAuthenticator("secret", dir, 1)
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	h, err := Handler(Options{Agent: testAgent(t), Store: StoreOf(database), Auth: auth})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Error("the cross-site guard is installed on a mount that has a token check, which would refuse a legitimate caller on another origin")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 from the token check", rec.Code)
	}
}

// Every response carries the headers, including one that answers 404. A route
// added later cannot be added without them, because they wrap the whole mount
// rather than each route.
func TestHandler_EveryResponseCarriesTheSecurityHeaders(t *testing.T) {
	h, err := Handler(Options{Agent: testAgent(t)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	for _, path := range []string{"/", "/api/v1/status", "/no/such/thing"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		h.ServeHTTP(rec, req)

		for _, header := range []string{
			"Content-Security-Policy",
			"X-Content-Type-Options",
			"X-Frame-Options",
			"Referrer-Policy",
		} {
			if rec.Header().Get(header) == "" {
				t.Errorf("%s carries no %s", path, header)
			}
		}
		// The one that stops an injected construct running even if the
		// sanitiser ever lets one through.
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
			t.Errorf("%s: the policy does not confine scripts to this origin: %q", path, csp)
		}
	}
}
