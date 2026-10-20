// Package httpapi exposes the JSON API and serves the embedded dashboard.
//
// The API is intentionally small and versioned under /api/v1. It is the only
// surface the frontend uses; the handlers know nothing about HTML.
package httpapi

import (
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/fmotalleb/pg-dump-dashboard/internal/dump"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/pgclient"
	"github.com/fmotalleb/pg-dump-dashboard/internal/postgres"
	"github.com/fmotalleb/pg-dump-dashboard/internal/session"
)

// Options configures the HTTP layer.
type Options struct {
	// Endpoint is the configured PostgreSQL "host:port" (no credentials).
	Endpoint string
	// ExecutionContext names the active backend.
	ExecutionContext string
	// Version is the build version reported by /api/v1/meta.
	Version string
	// MaxConcurrentDumps bounds simultaneous dump streams.
	MaxConcurrentDumps int
	// MaxRequestBytes bounds JSON request bodies.
	MaxRequestBytes int64
	// SessionTTL is the idle timeout of a dashboard session.
	SessionTTL time.Duration
	// CookieSecure marks the session cookie Secure. Enable it when the UI is
	// served over TLS (directly or through a trusted reverse proxy).
	CookieSecure bool
}

// Handler wires the API and the static frontend together.
type Handler struct {
	opts     Options
	baseConn pgclient.Conn
	discover *postgres.Discoverer
	dumps    *dump.Service
	sessions *session.Store
	logger   *slog.Logger
	static   fs.FS
	dumpSem  chan struct{}
}

// New builds the HTTP handler.
func New(opts Options, baseConn pgclient.Conn, discover *postgres.Discoverer, dumps *dump.Service,
	sessions *session.Store, logger *slog.Logger, static fs.FS,
) *Handler {
	if opts.MaxConcurrentDumps <= 0 {
		opts.MaxConcurrentDumps = 4
	}
	return &Handler{
		opts:     opts,
		baseConn: baseConn,
		discover: discover,
		dumps:    dumps,
		sessions: sessions,
		logger:   logger,
		static:   static,
		dumpSem:  make(chan struct{}, opts.MaxConcurrentDumps),
	}
}

// Routes returns the fully wrapped HTTP handler.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/healthz", h.handleHealth)
	mux.HandleFunc("GET /api/v1/meta", h.handleMeta)

	mux.HandleFunc("POST /api/v1/connect", h.handleConnect)
	mux.HandleFunc("GET /api/v1/session", h.handleSessionInfo)
	mux.HandleFunc("DELETE /api/v1/session", h.handleLogout)
	mux.HandleFunc("GET /api/v1/overview", h.handleOverview)
	mux.HandleFunc("POST /api/v1/overview/refresh", h.handleRefresh)
	mux.HandleFunc("GET /api/v1/databases", h.handleDatabases)
	mux.HandleFunc("GET /api/v1/databases/{name}", h.handleDatabase)
	mux.HandleFunc("GET /api/v1/databases/{name}/dump", h.handleDump)

	mux.Handle("/", h.staticHandler())
	mux.HandleFunc("/api/", h.handleNotFound)

	return chain(mux,
		h.recoverer,
		h.securityHeaders,
		h.accessLog,
	)
}

// chain applies middleware so that the first entry is the outermost wrapper.
func chain(next http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		next = middlewares[i](next)
	}
	return next
}

func (h *Handler) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, codeNotFound, "unknown API endpoint")
}
