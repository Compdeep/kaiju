//go:build noui

package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// What a build with no interface answers.
//
// Under the ordinary tags this file is not compiled, so `go test ./...` does
// not run it and `make test-noui` is what does. That is the cost of the tag:
// the two builds cannot both be tested by one command.

func TestWebUIHandler_RootSaysWhyThereIsNoPage(t *testing.T) {
	rec := httptest.NewRecorder()
	WebUIHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("root status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if !strings.Contains(rec.Body.String(), "noui") {
		t.Errorf("root body does not name the tag that caused it: %q", rec.Body.String())
	}
}

func TestWebUIHandler_OtherPathsAreAPlain404(t *testing.T) {
	rec := httptest.NewRecorder()
	WebUIHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/mistyped", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	// A caller who mistyped an API path must not be answered with a paragraph
	// about the web interface.
	if strings.Contains(rec.Body.String(), "noui") {
		t.Errorf("a mistyped path was answered with the interface explanation: %q", rec.Body.String())
	}
}
