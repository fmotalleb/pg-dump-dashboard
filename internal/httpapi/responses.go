package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/fmotalleb/pg-dump-dashboard/internal/postgres"
)

// Error codes returned in the "code" field of an error response.
const (
	codeInvalidRequest = "invalid_request"
	codeAuthFailed     = "auth_failed"
	codeUnreachable    = "unreachable"
	codeTimeout        = "timeout"
	codeClientMissing  = "client_missing"
	codeNotFound       = "not_found"
	codeForbidden      = "forbidden"
	codeNoSession      = "no_session"
	codeBusy           = "too_many_dumps"
	codeInternal       = "internal_error"
)

// errorResponse is the structured error envelope used by every endpoint.
type errorResponse struct {
	Error apiError `json:"error"`
}

// apiError describes a failure in a machine readable way.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, `{"error":{"code":"internal_error","message":"encode response"}}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: apiError{Code: code, Message: message}})
}

// statusFor maps a classified PostgreSQL error to an HTTP status code.
func statusFor(kind postgres.ErrorKind) (status int, code string) {
	switch kind {
	case postgres.KindAuth:
		return http.StatusUnauthorized, codeAuthFailed
	case postgres.KindUnreachable:
		return http.StatusBadGateway, codeUnreachable
	case postgres.KindTimeout:
		return http.StatusGatewayTimeout, codeTimeout
	case postgres.KindClientMissing:
		return http.StatusServiceUnavailable, codeClientMissing
	case postgres.KindNoDatabase:
		return http.StatusNotFound, codeNotFound
	case postgres.KindRestricted:
		return http.StatusForbidden, codeForbidden
	case postgres.KindInternal:
		return http.StatusInternalServerError, codeInternal
	default:
		return http.StatusInternalServerError, codeInternal
	}
}

// writePostgresError renders a classified PostgreSQL failure.
func writePostgresError(w http.ResponseWriter, err error) {
	status, code := statusFor(postgres.KindOf(err))
	writeError(w, status, code, postgres.Reason(err))
}
