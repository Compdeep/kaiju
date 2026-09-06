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
		addr: Loopback(addr),
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
 * Loopback confines a listen address to this machine.
 * desc: This server carries no TLS and its first visitor claims the node, so an
 *       address reaching further than the machine publishes both: a password
 *       typed over plain HTTP, and a setup page anyone can reach first.
 *
 *       A port with no host — ":8090", which is what a port number formats to —
 *       binds every interface. That is the ordinary way to write it and the
 *       reason this exists: the wide bind was never chosen, it was inherited
 *       from the shorthand.
 *
 *       Confined rather than refused, and said out loud, because a daemon that
 *       will not start over an address is worse than one that starts somewhere
 *       safe and explains. Reaching it from elsewhere is a tunnel's job, which
 *       is a decision made outside this process by somebody who meant it.
 * param: addr - the address as configured.
 * return: the address to bind, always on this machine.
 */
func Loopback(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// Not host:port at all. Left alone: this is not the place to reject a
		// malformed address, and Listen will say so more precisely.
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "*" {
		log.Printf("[gateway] %s reaches beyond this machine, and this server has no TLS "+
			"and lets its first visitor claim the node — binding 127.0.0.1:%s instead. "+
			"Use a tunnel to reach it from elsewhere.", addr, port)
		return net.JoinHostPort("127.0.0.1", port)
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		log.Printf("[gateway] %s reaches beyond this machine, and this server has no TLS "+
			"and lets its first visitor claim the node — binding 127.0.0.1:%s instead. "+
			"Use a tunnel to reach it from elsewhere.", addr, port)
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
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
