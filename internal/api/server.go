package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/resin-proxy/resin/internal/service"
)

// Server wraps the HTTP server and mux for the Resin API.
type Server struct {
	httpServer *http.Server
	mux        *http.ServeMux
}

// NewServer creates a new API server wired with all routes.
func NewServer(port int, adminToken string, systemSvc service.SystemService) *Server {
	mux := http.NewServeMux()

	// Public (no auth)
	mux.Handle("GET /healthz", HandleHealthz())

	// Authenticated routes
	authed := http.NewServeMux()
	authed.Handle("GET /api/v1/system/info", HandleSystemInfo(systemSvc))
	authed.Handle("GET /api/v1/system/config", HandleSystemConfig(systemSvc))

	mux.Handle("/api/", AuthMiddleware(adminToken, authed))

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	return &Server{
		httpServer: srv,
		mux:        mux,
	}
}

// ListenAndServe starts the HTTP server. It blocks until the server stops.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Handler returns the underlying http.Handler for testing.
func (s *Server) Handler() http.Handler {
	return s.mux
}
