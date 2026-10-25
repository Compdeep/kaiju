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
		Brand:    Brand{Name: "Acme", Attribution: true},
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
	if out.Brand.Name != "Acme" || !out.Brand.Attribution {
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
		// Whether this mount has a sign-in. Not a secret: a page that shows a
		// sign-in form already tells anyone looking that one exists, and a mount
		// without one answers 404 on the auth routes, which says the same thing
		// to anyone who asks. What it buys is the page not having to find out by
		// posting to a route that is not there.
		case "sign_in":
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

// The page is told what the server built, not what the caller declared.
//
// Without an Authenticator the auth routes are never registered, but the page
// assumed a sign-in always existed: it saw no token, redirected to its sign-in,
// and that page posted to /api/v1/auth/login — a route this mount does not have,
// answering 404. The flag is how it knows, so it is derived here and an
// application cannot set it wrongly.
func TestSignInReportsWhatTheMountActuallyHas(t *testing.T) {
	// An application claiming a sign-in it did not configure is overruled.
	h, err := Handler(Options{Agent: testAgent(t), Config: Config{SignIn: true}})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ConfigPath, nil))

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if v, ok := got["sign_in"]; !ok {
		t.Fatal("sign_in absent — false is the answer that matters and must reach the page")
	} else if v != false {
		t.Errorf("sign_in = %v, want false: no Authenticator was given", v)
	}

	// And the route the page would have posted to genuinely is not there, which
	// is why the flag exists.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader("{}")))
	if rec2.Code != http.StatusNotFound {
		t.Errorf("POST /api/v1/auth/login = %d, want 404 on a mount with no sign-in", rec2.Code)
	}
}
