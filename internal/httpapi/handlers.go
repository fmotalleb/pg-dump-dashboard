package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/fmotalleb/pg-dump-dashboard/internal/dump"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/pgclient"
	"github.com/fmotalleb/pg-dump-dashboard/internal/postgres"
	"github.com/fmotalleb/pg-dump-dashboard/internal/session"
)

const (
	maxUserLen     = 63  // PostgreSQL role names are at most NAMEDATALEN-1 bytes.
	maxPasswordLen = 512 // libpq truncates longer values anyway.
)

// connectRequest is the body of POST /api/v1/connect.
type connectRequest struct {
	User     string `json:"user"`
	Password string `json:"password"`
	// Database is optional; it selects the maintenance database used for
	// discovery and defaults to the server-wide configured value.
	Database string `json:"database"`
}

// sessionInfo is the public view of a session; it never contains credentials.
type sessionInfo struct {
	ExpiresAt time.Time `json:"expiresAt"`
	User      string    `json:"user"`
	Database  string    `json:"database"`
}

// connectResponse is returned by /connect and /overview.
type connectResponse struct {
	Session  sessionInfo        `json:"session"`
	Overview *postgres.Overview `json:"overview"`
}

// metaResponse describes the deployment; it is safe for anonymous callers.
type metaResponse struct {
	Version          string   `json:"version"`
	Endpoint         string   `json:"endpoint"`
	ExecutionContext string   `json:"executionContext"`
	SessionTTL       int      `json:"sessionTtlSeconds"`
	MaxDumps         int      `json:"maxConcurrentDumps"`
	DumpFormats      []string `json:"dumpFormats"`
	DefaultDatabase  string   `json:"defaultDatabase"`
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, metaResponse{
		Version:          h.opts.Version,
		Endpoint:         h.opts.Endpoint,
		ExecutionContext: h.opts.ExecutionContext,
		SessionTTL:       int(h.opts.SessionTTL.Seconds()),
		MaxDumps:         h.opts.MaxConcurrentDumps,
		DumpFormats:      []string{string(pgclient.FormatCustom), string(pgclient.FormatPlain)},
		DefaultDatabase:  h.baseConn.Database,
	})
}

func (h *Handler) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req connectRequest
	if err := h.decodeJSON(w, r, &req); err != nil {
		h.writeDecodeError(w, err)
		return
	}

	creds, err := h.credentials(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	overview, err := h.collectOverview(r.Context(), creds)
	if err != nil {
		h.logger.Warn("connect failed",
			slog.String("endpoint", h.opts.Endpoint),
			slog.String("user", creds.User),
			slog.String("error", postgres.Reason(err)),
		)
		writePostgresError(w, err)
		return
	}

	sess := h.sessions.Create(creds)
	sess.SetOverview(overview)
	h.setSessionCookie(w, sess.ID)

	h.logger.Info("session opened",
		slog.String("endpoint", h.opts.Endpoint),
		slog.String("user", creds.User),
		slog.Int("databases", len(overview.Databases)),
		slog.String("execution", h.opts.ExecutionContext),
	)

	writeJSON(w, http.StatusOK, connectResponse{
		Session:  h.sessionInfo(sess),
		Overview: overview,
	})
}

func (h *Handler) handleSessionInfo(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, connectResponse{Session: h.sessionInfo(sess), Overview: sess.Overview()})
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(session.CookieName); err == nil {
		h.sessions.Delete(cookie.Value)
	}
	h.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleOverview(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	overview := sess.Overview()
	if overview == nil {
		writeError(w, http.StatusNotFound, codeNotFound, "no metadata collected yet")
		return
	}
	writeJSON(w, http.StatusOK, connectResponse{Session: h.sessionInfo(sess), Overview: overview})
}

func (h *Handler) handleRefresh(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}

	conn := h.connFor(sess)
	overview, err := h.discover.Overview(r.Context(), conn)
	if err != nil {
		h.logger.Warn("metadata refresh failed",
			slog.String("endpoint", h.opts.Endpoint),
			slog.String("error", postgres.Reason(err)),
		)
		writePostgresError(w, err)
		return
	}
	sess.SetOverview(overview)

	writeJSON(w, http.StatusOK, connectResponse{Session: h.sessionInfo(sess), Overview: overview})
}

func (h *Handler) handleDatabases(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	overview := sess.Overview()
	if overview == nil {
		writeError(w, http.StatusNotFound, codeNotFound, "no metadata collected yet")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"databases":    overview.Databases,
		"totals":       overview.Totals,
		"collectedAt":  overview.CollectedAt,
		"endpoint":     overview.Endpoint,
		"objectCounts": overview.ObjectCountSource,
		"rowCounts":    overview.RowCountSource,
	})
}

func (h *Handler) handleDatabase(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}

	name := r.PathValue("name")
	if err := pgclient.ValidDatabaseName(name); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	db, found := sess.Database(name)
	if !found {
		writeError(w, http.StatusNotFound, codeNotFound, fmt.Sprintf("database %q is not visible to this session", name))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"database": db,
		"dump": map[string]any{
			"eligible": db.CanDump(),
			"formats":  []string{string(pgclient.FormatCustom), string(pgclient.FormatPlain)},
			"reason":   dumpRefusal(db),
		},
	})
}

// handleDump streams pg_dump output to the client.
func (h *Handler) handleDump(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}

	name := r.PathValue("name")
	if err := pgclient.ValidDatabaseName(name); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	db, found := sess.Database(name)
	if !found {
		writeError(w, http.StatusNotFound, codeNotFound, fmt.Sprintf("database %q is not visible to this session", name))
		return
	}
	if !db.CanDump() {
		writeError(w, http.StatusForbidden, codeForbidden, dumpRefusal(db))
		return
	}

	req, err := parseDumpRequest(r, name)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	// Bound concurrent dumps: a dump pins a server-side session and a
	// subprocess, so it must not be unbounded.
	select {
	case h.dumpSem <- struct{}{}:
		defer func() { <-h.dumpSem }()
	default:
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusTooManyRequests, codeBusy, "too many concurrent dumps, retry later")
		return
	}

	result, err := h.dumps.Stream(r.Context(), w, h.connFor(sess), req)
	if err == nil {
		return
	}

	switch {
	case result.Started:
		// Headers are gone; the only honest signal left is the log and a
		// truncated body. Report it and stop.
		h.logger.Warn("dump failed after streaming started",
			slog.String("database", name),
			slog.Int64("bytes", result.Bytes),
			slog.String("error", postgres.Reason(err)),
		)
	case errors.Is(err, dump.ErrClientGone):
		h.logger.Info("client cancelled the dump", slog.String("database", name))
	default:
		h.logger.Warn("dump rejected",
			slog.String("database", name),
			slog.String("error", postgres.Reason(err)),
		)
		writePostgresError(w, err)
	}
}

func dumpRefusal(db postgres.Database) string {
	if db.CanDump() {
		return ""
	}
	if db.Error != "" {
		return db.Error
	}
	if !db.AllowConnections {
		return "connections are disabled for this database"
	}
	if !db.ConnectPrivilege {
		return "the current role has no CONNECT privilege on this database"
	}
	return "this database is not available for dumping"
}

// parseDumpRequest validates the dump query parameters.
func parseDumpRequest(r *http.Request, database string) (dump.Request, error) {
	query := r.URL.Query()

	format := pgclient.DumpFormat(strings.ToLower(strings.TrimSpace(query.Get("format"))))
	if format == "" {
		format = pgclient.FormatCustom
	}
	if !format.Valid() {
		return dump.Request{}, fmt.Errorf("format must be %q or %q", pgclient.FormatCustom, pgclient.FormatPlain)
	}

	schema := strings.TrimSpace(query.Get("schema"))
	if schema != "" {
		if err := pgclient.ValidDatabaseName(schema); err != nil {
			return dump.Request{}, fmt.Errorf("schema: %w", err)
		}
	}

	return dump.Request{Database: database, Format: format, Schema: schema}, nil
}

// collectOverview runs discovery with the configured bounds.
func (h *Handler) collectOverview(ctx context.Context, creds session.Credentials) (*postgres.Overview, error) {
	conn := h.baseConn
	conn.User = creds.User
	conn.Password = creds.Password
	conn.Database = creds.Database

	return h.discover.Overview(ctx, conn)
}

func (h *Handler) connFor(sess *session.Session) pgclient.Conn {
	conn := h.baseConn
	conn.User = sess.Creds.User
	conn.Password = sess.Creds.Password
	conn.Database = sess.Creds.Database
	return conn
}

func (h *Handler) credentials(req connectRequest) (session.Credentials, error) {
	user := strings.TrimSpace(req.User)
	if err := validateUser(user); err != nil {
		return session.Credentials{}, err
	}
	if len(req.Password) > maxPasswordLen {
		return session.Credentials{}, fmt.Errorf("password must be at most %d bytes", maxPasswordLen)
	}

	database := strings.TrimSpace(req.Database)
	if database == "" {
		database = h.baseConn.Database
	}
	if err := pgclient.ValidDatabaseName(database); err != nil {
		return session.Credentials{}, err
	}

	return session.Credentials{
		User:     user,
		Password: execution.Secret(req.Password),
		Database: database,
	}, nil
}

// validateUser accepts the characters PostgreSQL allows in role names while
// rejecting control characters and anything that could be misread.
func validateUser(user string) error {
	if user == "" {
		return errors.New("user must not be empty")
	}
	if len(user) > maxUserLen {
		return fmt.Errorf("user must be at most %d bytes", maxUserLen)
	}
	for _, r := range user {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return errors.New("user must not contain whitespace or control characters")
		}
	}
	return nil
}

// decodeJSON reads a bounded JSON body, rejecting unknown fields.
func (h *Handler) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	contentType := r.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(contentType), "application/json") {
		return errUnsupportedMedia
	}

	body := http.MaxBytesReader(w, r.Body, h.opts.MaxRequestBytes)
	defer func() { _ = body.Close() }()

	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON body: trailing data after object")
	}
	return nil
}

var errUnsupportedMedia = errors.New("Content-Type must be application/json")

func (h *Handler) writeDecodeError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	code := codeInvalidRequest
	if errors.Is(err, errUnsupportedMedia) {
		status = http.StatusUnsupportedMediaType
	}
	writeError(w, status, code, err.Error())
}

// requireSession resolves the session cookie or writes an error response.
func (h *Handler) requireSession(w http.ResponseWriter, r *http.Request) (s *session.Session, ok bool) {
	cookie, err := r.Cookie(session.CookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, codeNoSession, "no active session: POST /api/v1/connect first")
		return nil, false
	}
	sess, found := h.sessions.Get(cookie.Value)
	if !found {
		h.clearSessionCookie(w)
		writeError(w, http.StatusUnauthorized, codeNoSession, "session expired: POST /api/v1/connect again")
		return nil, false
	}
	return sess, true
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, id string) {
	// The Secure flag is configurable because the dashboard is commonly served
	// over plain HTTP inside a trusted network; HttpOnly, SameSite=Strict and
	// the opaque random value protect the cookie itself.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is opt-in via -cookie-secure.

		Name:     session.CookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.opts.CookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.opts.SessionTTL.Seconds()),
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is opt-in via -cookie-secure.

		Name:     session.CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.opts.CookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

func (h *Handler) sessionInfo(sess *session.Session) sessionInfo {
	return sessionInfo{
		ExpiresAt: sess.ExpiresAt(h.sessions.TTL()),
		User:      sess.Creds.User,
		Database:  sess.Creds.Database,
	}
}
