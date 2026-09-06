package gateway

import "testing"

// The gateway confines itself to this machine.
//
// It carries no TLS and its first visitor claims the node, so an address
// reaching further publishes both: a password typed over plain HTTP, and a
// setup page anyone can reach first. This was not a hypothetical — the daemon
// formatted its address as ":port", which binds every interface, and ran that
// way with a configuration endpoint outside the token check.
func TestAWideAddressIsConfinedToThisMachine(t *testing.T) {
	for _, addr := range []string{
		":8090",          // the shorthand a port number formats to
		"0.0.0.0:8090",   // every interface, said outright
		"[::]:8090",      // and the same in IPv6
		"192.168.1.5:80", // a real interface on this host
		"10.0.0.7:8090",
	} {
		got := Loopback(addr)
		if got != "127.0.0.1:8090" && got != "127.0.0.1:80" {
			t.Errorf("Loopback(%q) = %q, which still reaches beyond this machine", addr, got)
		}
	}
}

// An address already confined is left exactly as it was, so a deployment that
// chose one keeps it — including a port a caller is relying on.
func TestALoopbackAddressIsLeftAlone(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8090", "localhost:8090", "[::1]:8090"} {
		if got := Loopback(addr); got != addr {
			t.Errorf("Loopback(%q) = %q; a confined address must not be rewritten", addr, got)
		}
	}
}

// The port always survives. Confining an address must not move the daemon to a
// port nobody is pointed at.
func TestThePortIsNeverChanged(t *testing.T) {
	for addr, want := range map[string]string{
		":1234":        "127.0.0.1:1234",
		"0.0.0.0:9999": "127.0.0.1:9999",
	} {
		if got := Loopback(addr); got != want {
			t.Errorf("Loopback(%q) = %q, want %q", addr, got, want)
		}
	}
}

// A malformed address is left for the listener to reject, which says what is
// wrong with it more precisely than this could.
func TestAMalformedAddressIsLeftForTheListener(t *testing.T) {
	for _, addr := range []string{"not-an-address", ""} {
		if got := Loopback(addr); got != addr {
			t.Errorf("Loopback(%q) = %q; it should pass through untouched", addr, got)
		}
	}
}

// And the constructor applies it, or none of the above reaches the socket.
func TestTheServerBindsWhatLoopbackReturns(t *testing.T) {
	if got := New(":8090").addr; got != "127.0.0.1:8090" {
		t.Errorf("New(\":8090\") binds %q", got)
	}
}
