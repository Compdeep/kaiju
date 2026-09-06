package gateway

import "testing"

// A port with no host is confined, whether the node is claimed or not.
//
// Nobody chooses this address: it is what a port number formats to, and it
// binds every interface. That is how the daemon came to be published without
// anyone deciding to publish it — with a configuration endpoint outside the
// token check, handing out provider keys.
func TestAPortWithNoHostIsAlwaysConfined(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		if got := BindAddr(":8090", claimed); got != "127.0.0.1:8090" {
			t.Errorf("BindAddr(\":8090\", claimed=%v) = %q; a port alone is not a decision to publish", claimed, got)
		}
	}
}

// An unclaimed node is confined however it is configured. Publishing one gives
// it away: whoever arrives first sets the password and owns it.
func TestAnUnclaimedNodeIsConfinedWhateverIsConfigured(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8090", "[::]:8090", "192.168.1.5:8090", "10.0.0.7:8090"} {
		if got := BindAddr(addr, false); got != "127.0.0.1:8090" {
			t.Errorf("BindAddr(%q, unclaimed) = %q; an unclaimed node must not be published", addr, got)
		}
	}
}

// A claimed node binds where it was told. The operator asked, and has an account
// standing between the network and the interface.
func TestAClaimedNodeBindsWhereItWasTold(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8090", "192.168.1.5:8090"} {
		if got := BindAddr(addr, true); got != addr {
			t.Errorf("BindAddr(%q, claimed) = %q; a configured host is a decision", addr, got)
		}
	}
}

// An address already confined is left exactly as it was, so a deployment that
// chose one keeps it — including a port a caller is relying on.
func TestALoopbackAddressIsLeftAlone(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8090", "localhost:8090", "[::1]:8090"} {
		for _, claimed := range []bool{false, true} {
			if got := BindAddr(addr, claimed); got != addr {
				t.Errorf("BindAddr(%q, claimed=%v) = %q; a confined address must not be rewritten", addr, claimed, got)
			}
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
		if got := BindAddr(addr, false); got != want {
			t.Errorf("BindAddr(%q) = %q, want %q", addr, got, want)
		}
	}
}

// A malformed address is left for the listener to reject, which says what is
// wrong with it more precisely than this could.
func TestAMalformedAddressIsLeftForTheListener(t *testing.T) {
	for _, addr := range []string{"not-an-address", ""} {
		if got := BindAddr(addr, true); got != addr {
			t.Errorf("BindAddr(%q) = %q; it should pass through untouched", addr, got)
		}
	}
}

// And the constructor binds exactly what it is given, so the decision lives in
// one place rather than being made twice with a chance to differ.
func TestTheServerBindsWhatItIsGiven(t *testing.T) {
	if got := New("127.0.0.1:8090").addr; got != "127.0.0.1:8090" {
		t.Errorf("New binds %q", got)
	}
	if got := New("0.0.0.0:8090").addr; got != "0.0.0.0:8090" {
		t.Errorf("New rewrote its address to %q; BindAddr decides, not this", got)
	}
}
