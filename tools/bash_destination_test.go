package tools

import "testing"

// Two commands reaching one host are spaced; everything else is not.
func TestBashDestination(t *testing.T) {
	b := NewBash("sh", t.TempDir())
	for _, c := range []struct{ name, cmd, want string }{
		{"the run that prompted this", `curl -s "https://ssd.jpl.nasa.gov/api/horizons.api?format=text&COMMAND='10'" | head -100`, "ssd.jpl.nasa.gov"},
		{"case and port are not the host", "curl -sL HTTPS://SSD.JPL.NASA.GOV:8443/api", "ssd.jpl.nasa.gov"},
		{"a second URL does not decide", "curl -s https://a.example/x && curl -s https://b.example/y", "a.example"},
		{"unquoted, ended by a pipe", "curl -sL https://compdeep.com/js/app.js | grep rabbit", "compdeep.com"},
		{"a redirect ends it too", "curl -s https://compdeep.com/a.js>out.txt", "compdeep.com"},
		{"no URL is never delayed", "grep -c rabbit fetched/app.js", ""},
		{"a bare word is not a URL", "echo https-is-not-a-url", ""},
		{"no command at all", "", ""},
	} {
		if got := b.Destination(map[string]any{"command": c.cmd}); got != c.want {
			t.Errorf("%s:\n  command %q\n  got %q, want %q", c.name, c.cmd, got, c.want)
		}
	}
	if b.Throttle() == 0 {
		t.Error("bash declares no gap, so two commands to one host would still leave together")
	}
}
