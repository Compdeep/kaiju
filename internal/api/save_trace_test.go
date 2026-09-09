package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/internal/auth"
	"github.com/Compdeep/kaiju/internal/db"
	"github.com/Compdeep/kaiju/internal/gateway"
)

// The browser posts the nodes it watched a run produce. It has to say which
// message they are the trace of: naming none meant "the newest assistant
// message", and a one-node interjection then replaced the trace of the run that
// had done the work.

// The handler is reached through the real auth middleware, because that is the
// only way to put claims in the context — the key is unexported, deliberately.
func traceAPI(t *testing.T) (http.Handler, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	if err := database.CreateUser("u1", "pw-long-enough", 100, nil); err != nil {
		t.Fatalf("create user: %v", err)
	}
	svc, err := auth.NewJWTService("test-secret-value-long-enough", dir, 24)
	if err != nil {
		t.Fatalf("token service: %v", err)
	}
	user, err := database.GetUser("u1")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	token, _, err := svc.Issue(user)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	testToken = token

	a := &API{db: database}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/sessions/{id}/trace", a.handleSaveTrace)
	return gateway.WithJWTAuth(svc)(mux), database
}

var testToken string

func postTrace(t *testing.T, h http.Handler, session, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+session+"/trace", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func seedSessionWithAnswers(t *testing.T, d *db.DB) (first, second int64) {
	t.Helper()
	if err := d.CreateSession("s1", "web", "u1", "t"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	var err error
	if first, err = d.AddMessage("s1", "assistant", "first"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if second, err = d.AddMessage("s1", "assistant", "second"); err != nil {
		t.Fatalf("second: %v", err)
	}
	return first, second
}

func traceOfMessage(t *testing.T, d *db.DB, id int64) string {
	t.Helper()
	msgs, err := d.GetMessages("s1", 100)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, m := range msgs {
		if m.ID == id {
			return m.DAGTrace
		}
	}
	t.Fatalf("message %d not found", id)
	return ""
}

// A body with no message_id is refused rather than guessed at.
func TestSaveTrace_RefusesABodyThatNamesNoMessage(t *testing.T) {
	h, d := traceAPI(t)
	first, _ := seedSessionWithAnswers(t, d)

	rec := postTrace(t, h, "s1", `{"nodes":[{"id":"n1"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if got := traceOfMessage(t, d, first); got != "" {
		t.Errorf("a trace naming no message was written anyway: %q", got)
	}
}

// Named, it lands where it was told and nowhere else.
func TestSaveTrace_WritesToTheMessageNamed(t *testing.T) {
	h, d := traceAPI(t)
	first, second := seedSessionWithAnswers(t, d)

	rec := postTrace(t, h, "s1", `{"message_id":`+itoa(first)+`,"nodes":[{"id":"n1"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := traceOfMessage(t, d, first); got == "" {
		t.Error("the named message has no trace")
	}
	if got := traceOfMessage(t, d, second); got != "" {
		t.Errorf("the newest message was written to instead: %q", got)
	}
}

// A message id belonging to another conversation is refused, not written.
func TestSaveTrace_RefusesAMessageFromAnotherSession(t *testing.T) {
	h, d := traceAPI(t)
	first, _ := seedSessionWithAnswers(t, d)
	if err := d.CreateSession("s2", "web", "u1", "other"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	rec := postTrace(t, h, "s2", `{"message_id":`+itoa(first)+`,"nodes":[{"id":"leaked"}]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if got := traceOfMessage(t, d, first); got != "" {
		t.Errorf("another session's trace reached it: %q", got)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}
