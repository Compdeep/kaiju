package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/agent/toolapi"
)

var errNotKept = errors.New("nowhere to keep it")

// A tool the tests can move between states.
type dummyTool struct{ name string }

func (d dummyTool) Name() string                { return d.name }
func (d dummyTool) Description() string         { return "a tool for the tests" }
func (d dummyTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (d dummyTool) Impact(map[string]any) int   { return 0 }
func (d dummyTool) Execute(ctx context.Context, p map[string]any) (string, error) {
	return "", nil
}

func patch(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	h.ServeHTTP(rec, req)
	return rec
}

// With nowhere to keep a change, the panel is read-only: no route at all,
// rather than one that forgets.
func TestToolState_NoWriterMeansNoRoute(t *testing.T) {
	h, err := Handler(Options{Agent: testAgent(t)})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if rec := patch(t, h, "/api/v1/tools/anything", `{"reach":"off"}`); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 — the panel should not be writable without somewhere to keep it", rec.Code)
	}
}

func TestToolState_OffAndLocalAreAccepted(t *testing.T) {
	ag := testAgent(t)
	if err := ag.Registry().Register(dummyTool{name: "probe_tool"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	var kept []string
	h, err := Handler(Options{
		Agent:        ag,
		SetToolState: func(name, state string) error { kept = append(kept, name+"="+state); return nil },
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	for _, want := range []string{"off", "local"} {
		rec := patch(t, h, "/api/v1/tools/probe_tool", `{"reach":"`+want+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("setting %s: status = %d, body %s", want, rec.Code, rec.Body.String())
		}
		if got := ag.Registry().ReachOf("probe_tool").String(); got != want {
			t.Errorf("registry says %s, want %s", got, want)
		}
	}
	if len(kept) != 2 || kept[0] != "probe_tool=off" || kept[1] != "probe_tool=local" {
		t.Errorf("the application was told %v", kept)
	}
}

// The rule the whole thing exists for.
func TestToolState_ReachBeyondThisMachineIsNeitherGrantedNorWithdrawn(t *testing.T) {
	ag := testAgent(t)
	if err := ag.Registry().Register(dummyTool{name: "shared_tool"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	h, err := Handler(Options{
		Agent:        ag,
		SetToolState: func(string, string) error { return nil },
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	// Granting it from here.
	if rec := patch(t, h, "/api/v1/tools/shared_tool", `{"reach":"everywhere"}`); rec.Code != http.StatusForbidden {
		t.Errorf("granting reach beyond this machine: status = %d, want 403", rec.Code)
	}

	// And withdrawing it, once something else has granted it.
	if err := ag.SetToolReach("shared_tool", toolapi.ReachEverywhere); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if rec := patch(t, h, "/api/v1/tools/shared_tool", `{"reach":"off"}`); rec.Code != http.StatusForbidden {
		t.Errorf("withdrawing it: status = %d, want 403", rec.Code)
	}
	if got := ag.Registry().ReachOf("shared_tool"); got != toolapi.ReachEverywhere {
		t.Errorf("it was changed anyway: %s", got)
	}
}

func TestToolState_UnknownToolAndBadBody(t *testing.T) {
	h, err := Handler(Options{
		Agent:        testAgent(t),
		SetToolState: func(string, string) error { return nil },
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if rec := patch(t, h, "/api/v1/tools/no_such_tool", `{"reach":"off"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown tool: status = %d, want 404", rec.Code)
	}
	if rec := patch(t, h, "/api/v1/tools/x", `{"reach":"sideways"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("nonsense state: status = %d, want 400", rec.Code)
	}
}

// A change that could not be kept is reported, not hidden: it is real until
// this process restarts, and an operator told nothing would find it undone.
func TestToolState_SaysWhenTheChangeCouldNotBeKept(t *testing.T) {
	ag := testAgent(t)
	if err := ag.Registry().Register(dummyTool{name: "unkept_tool"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	h, err := Handler(Options{
		Agent:        ag,
		SetToolState: func(string, string) error { return errNotKept },
	})
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}

	rec := patch(t, h, "/api/v1/tools/unkept_tool", `{"reach":"off"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Kept bool `json:"kept"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Kept {
		t.Error("the answer claims the change was kept when the application could not keep it")
	}
}
