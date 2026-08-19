package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestZeroConfig_SectionsAreAllOff(t *testing.T) {
	var c Config
	if c.Sections.Users || c.Sections.Workspace {
		t.Fatalf("an application that supplies nothing got a section: %+v", c.Sections)
	}
}

func TestAllSections_TurnsEveryOneOn(t *testing.T) {
	s := AllSections()
	if !s.Users || !s.Workspace {
		t.Fatalf("AllSections left one off: %+v", s)
	}
}

func TestValidate_RejectsAValueThatWouldEndTheDeclaration(t *testing.T) {
	// The value below closes the rule it is written into and opens another.
	c := Config{Theme: Theme{Light: map[string]string{"--accent": "red;} body{display:none"}}}
	if err := c.Validate(); err == nil {
		t.Fatal("a value carrying a brace and a semicolon was accepted")
	}
}

func TestValidate_RejectsANameThatIsNotACustomProperty(t *testing.T) {
	c := Config{Theme: Theme{Dark: map[string]string{"background": "#000"}}}
	if err := c.Validate(); err == nil {
		t.Fatal("a plain property name was accepted as a token")
	}
}

func TestValidate_RejectsAnUnknownDefaultMode(t *testing.T) {
	c := Config{Theme: Theme{Default: "sepia"}}
	if err := c.Validate(); err == nil {
		t.Fatal("an unknown default mode was accepted")
	}
}

func TestValidate_AcceptsAnOrdinaryTheme(t *testing.T) {
	c := Config{Theme: Theme{
		Light:   map[string]string{"--accent": "#2F6FED", "--bg": "rgb(246, 245, 243)"},
		Dark:    map[string]string{"--accent": "#38BDF8"},
		Default: "dark",
	}}
	if err := c.Validate(); err != nil {
		t.Fatalf("an ordinary theme was refused: %v", err)
	}
}

func TestConfigHandler_RefusesAConfigItCannotServe(t *testing.T) {
	_, err := ConfigHandler(Config{Theme: Theme{Light: map[string]string{"--x": "a;b"}}})
	if err == nil {
		t.Fatal("the handler was built over a theme Validate rejects")
	}
}

func TestConfigHandler_ServesWhatItWasGiven(t *testing.T) {
	in := Config{
		Brand:    Brand{Name: "Enbarr", Attribution: true},
		Sections: Sections{Users: false, Workspace: true},
	}
	h, err := ConfigHandler(in)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ConfigPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var out Config
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("the body is not the configuration: %v", err)
	}
	if out.Brand.Name != "Enbarr" || !out.Brand.Attribution {
		t.Errorf("brand did not survive the round trip: %+v", out.Brand)
	}
	if out.Sections.Users || !out.Sections.Workspace {
		t.Errorf("sections did not survive the round trip: %+v", out.Sections)
	}
}

// Nothing secret may be added to this type: the handler is served before
// anyone has a token, and the sign-in page reads it.
func TestServedConfig_CarriesNothingButBrandThemeAndSections(t *testing.T) {
	h, err := ConfigHandler(Config{Brand: Brand{Name: "x"}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ConfigPath, nil))

	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for k := range raw {
		switch k {
		case "brand", "theme", "sections":
		default:
			t.Errorf("an unauthenticated response carries %q; if that field is ever secret this endpoint leaks it", k)
		}
	}
}

func TestConfigHandler_RefusesAnythingButGET(t *testing.T) {
	h, err := ConfigHandler(Config{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, m := range []string{http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, ConfigPath, strings.NewReader("{}")))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d, want 405 — a theme that a request can set is a stylesheet a request can set", m, rec.Code)
		}
	}
}
