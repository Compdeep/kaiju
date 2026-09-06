package gateway

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

/*
 * Server is the kaiju HTTP gateway.
 * desc: Wraps an http.Server with a ServeMux, providing start and graceful shutdown methods.
 */
type Server struct {
	httpServer *http.Server
	mux        *http.ServeMux
	addr       string
}

/*
 * New creates a gateway server on the given address.
 * desc: Initializes an HTTP server with sensible timeouts and a fresh ServeMux.
 * param: addr - the listen address (e.g. ":8080")
 * return: a configured Server ready to register routes and start
 */
func New(addr string) *Server {
	mux := http.NewServeMux()
	return &Server{
		mux:  mux,
		addr: addr,
		httpServer: &http.Server{
			Addr:         addr,
			Handler:      mux,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 0, // disabled — execute endpoint blocks for full DAG duration (5m+); the DAG wall clock is the authority
			IdleTimeout:  120 * time.Second,
		},
	}
}

/*
 * BindAddr decides where the interface listens.
 * desc: Loopback unless an operator has both asked for wider and earned it.
 *
 *       Asked for: a host written in the configuration. A port alone formats to
 *       ":8090", which binds every interface — that is the ordinary way to write
 *       a port and it was how this server came to be published without anyone
 *       choosing it. So the shorthand is read as "no opinion" and confined, and
 *       only a host somebody typed counts as a decision.
 *
 *       Earned it: the node already has an account. An unclaimed node hands
 *       ownership to its first visitor, so publishing one is giving it away.
 *       This is the half that cannot be waived, and the reason the two changes
 *       arrived together.
 *
 *       What is NOT checked is transport, because there is none to check: this
 *       server has no TLS, so a wide bind sends the password and the session
 *       token in clear. That is said, loudly, and left to the operator — they
 *       own the machine and may have a firewall in front of it, and a daemon
 *       that refuses to start is worse than one that starts and explains.
 * param: addr - the address as configured.
 * param: claimed - whether the node already has an account.
 * return: the address to bind.
 */
func BindAddr(addr string, claimed bool) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Not host:port at all. Left alone: this is not the place to reject a
		// malformed address, and Listen will say so more precisely.
		return addr
	}
	confine := func(why string) string {
		log.Printf("[gateway] %s reaches beyond this machine — %s. Binding 127.0.0.1:%s instead; "+
			"use a tunnel, or set channels.web.host once the node has an account.", addr, why, port)
		return net.JoinHostPort("127.0.0.1", port)
	}
	switch {
	case host == "":
		// A port with no host. Nobody chose this; it is what a port number
		// formats to.
		return confine("no host was configured, and a port alone binds every interface")
	case isLoopback(host):
		return addr
	case !claimed:
		return confine("this node has no account yet, so publishing it would let " +
			"whoever arrives first claim it")
	}
	log.Printf("[gateway] listening on %s, which reaches beyond this machine. There is no TLS "+
		"here, so the password and session token travel in clear — restrict who can reach "+
		"this port.", addr)
	return addr
}

// isLoopback reports whether a configured host stays on this machine.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

/*
 * Mux returns the underlying ServeMux for registering handlers.
 * desc: Provides access to the mux so callers can mount routes before starting the server.
 * return: the server's http.ServeMux
 */
func (s *Server) Mux() *http.ServeMux {
	return s.mux
}

/*
 * Start begins listening and blocks until the server exits.
 * desc: Opens a TCP listener on the configured address and serves HTTP requests.
 * return: an error if the listener fails to bind or the server encounters a fatal error
 */
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("gateway: listen %s: %w", s.addr, err)
	}
	log.Printf("[gateway] listening on %s", ln.Addr())
	return s.httpServer.Serve(ln)
}

/*
 * Shutdown gracefully stops the server.
 * desc: Signals the server to stop accepting new connections and waits for in-flight requests to complete.
 * param: ctx - context controlling the shutdown deadline
 * return: an error if the shutdown does not complete before the context is cancelled
 */
func (s *Server) Shutdown(ctx context.Context) error {
	log.Println("[gateway] shutting down...")
	return s.httpServer.Shutdown(ctx)
}
