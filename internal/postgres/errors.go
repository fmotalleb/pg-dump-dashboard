package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
)

// ErrorKind classifies failures so the HTTP layer can pick a status code.
type ErrorKind int

// Error kinds surfaced by the discovery and dump layers.
const (
	// KindInternal is an unexpected failure.
	KindInternal ErrorKind = iota
	// KindAuth means PostgreSQL rejected the credentials.
	KindAuth
	// KindUnreachable means the server could not be contacted.
	KindUnreachable
	// KindTimeout means the operation exceeded its deadline.
	KindTimeout
	// KindNoDatabase means the requested database does not exist.
	KindNoDatabase
	// KindClientMissing means psql/pg_dump is absent in the execution context.
	KindClientMissing
	// KindRestricted means PostgreSQL refused an operation (privileges).
	KindRestricted
)

// Error carries a classified failure with a message that is safe to return to
// a client.
type Error struct {
	Kind ErrorKind
	// Message is safe to display: it never contains credentials.
	Message string
	Err     error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

// Unwrap exposes the cause.
func (e *Error) Unwrap() error { return e.Err }

// KindOf extracts the classification of err, defaulting to KindInternal.
func KindOf(err error) ErrorKind {
	var pgErr *Error
	if errors.As(err, &pgErr) {
		return pgErr.Kind
	}
	return KindInternal
}

// messageFragments maps postgres/psql diagnostics to error kinds. Matching is
// case-insensitive and performed on redacted text only.
var messageFragments = []struct {
	kind     ErrorKind
	fragment string
}{
	{KindAuth, "password authentication failed"},
	{KindAuth, "no password supplied"},
	{KindAuth, "pg_hba.conf"},
	{KindAuth, "authentication failed"},
	{KindAuth, "not supported authentication"},
	{KindNoDatabase, "does not exist"},
	{KindUnreachable, "could not translate host name"},
	{KindUnreachable, "connection refused"},
	{KindUnreachable, "could not connect"},
	{KindUnreachable, "no route to host"},
	{KindUnreachable, "network is unreachable"},
	{KindUnreachable, "connection timed out"},
	{KindUnreachable, "server closed the connection"},
	{KindUnreachable, "timeout expired"},
	{KindUnreachable, "connection to server"},
	{KindRestricted, "permission denied"},
	{KindRestricted, "must be superuser"},
	{KindRestricted, "must be owner"},
}

// Classify converts any error returned by an execution backend into an *Error.
// The message is derived from redacted tool output and truncated to one line.
func Classify(operation string, err error) *Error {
	if err == nil {
		return nil
	}

	if errors.Is(err, execution.ErrBinaryNotFound) {
		return &Error{
			Kind:    KindClientMissing,
			Message: operation + ": PostgreSQL client tools are not available in the configured execution context",
			Err:     err,
		}
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &Error{Kind: KindTimeout, Message: operation + ": timed out", Err: err}
	}

	detail := errorDetail(err)
	lower := strings.ToLower(detail)

	if detail == "" {
		detail = "command failed"
	}

	for _, m := range messageFragments {
		if strings.Contains(lower, m.fragment) {
			return &Error{Kind: m.kind, Message: operation + ": " + detail, Err: err}
		}
	}

	return &Error{Kind: KindInternal, Message: operation + ": " + detail, Err: err}
}

// errorDetail extracts the most relevant, already redacted line from a failed
// command. Tool stderr is preferred over the wrapped error message, which
// repeats the whole argument vector.
func errorDetail(err error) string {
	var exitErr *execution.ExitError
	if errors.As(err, &exitErr) && exitErr.Stderr != "" {
		return execution.SalientLine(exitErr.Stderr)
	}
	return execution.SalientLine(err.Error())
}

// Reason returns a short explanation for a database that could not be
// inspected, or an empty string.
func Reason(err error) string {
	if err == nil {
		return ""
	}
	var pgErr *Error
	if errors.As(err, &pgErr) {
		return pgErr.Message
	}
	return execution.SalientLine(err.Error())
}
