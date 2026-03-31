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
			WriteTimeout: 120 * time.Second,
			IdleTimeout:  120 * time.Second,
		},
	}
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
