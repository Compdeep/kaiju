package main

import (
	"os"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/internal/configapi"
)

// The configuration endpoint is behind the same token check as everything else.
//
// It was outside it for initial setup, because nothing else could create the
// first account. What that left open was a read returning provider API keys and
// a write that could repoint the model endpoint — applied live and persisted —
// to anyone who could reach the port, on a server binding every interface.
//
// This reads the assembly rather than starting a daemon: the fault would be a
// route mounted on the bare mux, which is textual.
func TestTheConfigEndpointIsMountedBehindTheTokenCheck(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	if strings.Contains(body, "configAPI.RegisterRoutes(mux)") {
		t.Fatal("the configuration endpoint is mounted on the bare mux, so it answers " +
			"without a token — a read returns provider keys and a write repoints the model")
	}
	if !strings.Contains(body, "protect(configMux)") {
		t.Error("the configuration endpoint is not wrapped in the token check")
	}
}

// Every route it serves is wrapped, not the ones someone remembered. A route
// added to RegisterRoutes and left out of the mounting answers unprotected.
func TestEveryConfigRouteIsWrapped(t *testing.T) {
	src, err := os.ReadFile("../../internal/configapi/configapi.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	// What RegisterRoutes actually mounts.
	registered := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, "mux.HandleFunc(") {
			continue
		}
		i := strings.Index(line, "/api/")
		j := strings.Index(line[i:], `"`)
		if i < 0 || j < 0 {
			continue
		}
		registered[line[i:i+j]] = true
	}
	if len(registered) == 0 {
		t.Fatal("no routes found; this test is looking in the wrong place")
	}

	listed := map[string]bool{}
	for _, p := range configapi.Paths {
		listed[p] = true
	}
	for path := range registered {
		if !listed[path] {
			t.Errorf("%s is registered and not in configapi.Paths, so whatever the "+
				"caller wrapped the others in does not reach it", path)
		}
	}
	for path := range listed {
		if !registered[path] {
			t.Errorf("%s is listed for wrapping and no longer registered", path)
		}
	}
}
