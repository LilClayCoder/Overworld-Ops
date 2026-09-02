package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/calebgrabowski/overworld-ops/backend/internal/config"
	"github.com/calebgrabowski/overworld-ops/backend/internal/docker"
	"github.com/calebgrabowski/overworld-ops/backend/internal/ports"
	"github.com/calebgrabowski/overworld-ops/backend/internal/store"
)

// Server wires the HTTP handlers to the store, the Docker engine and the port
// allocator.
type Server struct {
	cfg    *config.Config
	store  *store.Store
	docker *docker.Client
	ports  *ports.Allocator
}

// New builds the API server.
func New(cfg *config.Config, st *store.Store, dk *docker.Client, alloc *ports.Allocator) *Server {
	return &Server{cfg: cfg, store: st, docker: dk, ports: alloc}
}

// Routes returns the HTTP handler for the whole API. It uses the standard
// library's ServeMux, whose method+pattern routing covers everything this API
// needs without pulling in a router dependency.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Health check, unauthenticated so a monitor can hit it.
	mux.HandleFunc("GET /api/health", s.handleHealth)

	// Auth.
	mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/auth/me", s.requireAuth(s.handleMe))

	// Server registry and lifecycle.
	mux.HandleFunc("GET /api/servers", s.requireAuth(s.handleListServers))
	mux.HandleFunc("POST /api/servers", s.requireAuth(s.handleCreateServer))
	mux.HandleFunc("GET /api/servers/{id}", s.requireAuth(s.handleGetServer))
	mux.HandleFunc("PATCH /api/servers/{id}", s.requireAuth(s.handleUpdateServer))
	mux.HandleFunc("DELETE /api/servers/{id}", s.requireAuth(s.handleDeleteServer))
	mux.HandleFunc("POST /api/servers/{id}/start", s.requireAuth(s.handleStartServer))
	mux.HandleFunc("POST /api/servers/{id}/stop", s.requireAuth(s.handleStopServer))

	// Live console. SSE rather than websockets: the console is one-directional
	// for now, and EventSource reconnects on its own.
	mux.HandleFunc("GET /api/servers/{id}/logs", s.requireAuth(s.handleServerLogs))

	return s.withCORS(s.withLogging(mux))
}

// handleHealth reports API liveness and whether Docker is reachable, so a
// broken Docker socket is visible without reading logs.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{"status": "ok", "docker": "ok"}
	status := http.StatusOK

	if err := s.docker.Ping(r.Context()); err != nil {
		body["docker"] = "unreachable"
		body["status"] = "degraded"
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, body)
}

// withLogging records one line per request at debug level, plus the duration.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		slog.Debug("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start))
	})
}

// withCORS allows the SvelteKit dev server to call the API with cookies. In a
// production setup the frontend is served from the same origin and
// OWO_CORS_ORIGIN can be left empty.
func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.CORSOrigin != "" && r.Header.Get("Origin") == s.cfg.CORSOrigin {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", s.cfg.CORSOrigin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type")
			h.Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the response status for the log line.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the wrapped writer so SSE streaming still works through
// the logging middleware.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
