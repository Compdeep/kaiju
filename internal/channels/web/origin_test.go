package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A websocket handshake is an ordinary request, and once it is accepted the
// page at the other end can talk to the agent freely. So which pages may open
// one is the whole question.
func TestSameHost(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		host   string
		want   bool
	}{
		{"the page itself", "http://127.0.0.1:7779", "127.0.0.1:7779", true},
		{"another port on this machine", "http://127.0.0.1:9999", "127.0.0.1:7779", false},
		{"another site entirely", "https://evil.example", "127.0.0.1:7779", false},
		{"no origin at all, so not a browser", "", "127.0.0.1:7779", true},
		{"an origin that will not parse", "://nonsense", "127.0.0.1:7779", false},
	}

	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := sameHost(r); got != c.want {
			t.Errorf("%s: sameHost = %v, want %v", c.name, got, c.want)
		}
	}
}
