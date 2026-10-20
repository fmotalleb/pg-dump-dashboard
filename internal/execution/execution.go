// Package execution defines the abstraction used to run PostgreSQL client
// commands (psql and pg_dump) in a concrete execution context.
//
// The application never talks to a database driver directly: every interaction
// with PostgreSQL happens by running a client command inside an execution
// context. Three contexts are anticipated:
//
//   - local:  the environment the web application itself runs in;
//   - docker: inside an explicitly configured container, reached through the
//     Docker CLI;
//   - ssh:    (future) a remote host, reached through an SSH connection.
//
// Adding a new context means implementing Runner and wiring it in
// internal/app. Handlers, metadata discovery and dump orchestration stay
// untouched because they only depend on this package.
package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Command is a single external command invocation. It is always expressed as
// an argument vector, never as a shell string, so no interpolation happens.
//
// Args must never contain credentials: secrets belong in Env, which is not
// exposed through the process list the way argv is.
type Command struct {
	// Path is the executable to run, for example "psql" or "/usr/bin/docker".
	Path string
	// Args are passed to the executable verbatim.
	Args []string
	// Env holds additional "KEY=VALUE" entries appended to the inherited
	// environment. Entries may carry secrets (PGPASSWORD).
	Env []string
	// Stdin is optional content written to the process standard input.
	Stdin string
}

// Describe returns a log-safe description of the command. It never contains
// environment values and therefore never leaks credentials.
func (c Command) Describe() string {
	parts := make([]string, 0, len(c.Args)+1)
	parts = append(parts, c.Path)
	parts = append(parts, c.Args...)
	return strings.Join(parts, " ")
}

// Result is the captured output of a finished command.
type Result struct {
	Stdout []byte
	Stderr []byte
}

// Process is a command that is already running. The embedded reader yields the
// standard output stream; standard error is captured (bounded) by the runner
// and reported through Wait.
//
// Callers own the process and must always call Close (or Wait) exactly once so
// the child is reaped and the pipes are released.
type Process interface {
	io.Reader

	// Kill terminates the process and any child process it spawned. It is
	// idempotent and does not block.
	Kill()

	// Wait blocks until the process exits, releases all resources and returns
	// an *ExitError when the command failed.
	Wait() error

	// Stderr returns the standard error captured so far. It is only complete
	// after Wait has returned.
	Stderr() string
}

// Runner starts PostgreSQL client commands in a specific execution context.
//
// Implementations must:
//   - honour context cancellation by terminating the process group;
//   - never place secrets from Command.Env into the argument vector;
//   - bound the amount of captured standard error;
//   - return *ExitError for non-zero exits and a wrapped ErrBinaryNotFound when
//     the executable is missing.
type Runner interface {
	// Name identifies the execution context ("local", "docker", "ssh").
	Name() string

	// Run executes a command to completion and captures its output.
	Run(ctx context.Context, cmd Command) (Result, error)

	// Start starts a command and returns a streaming handle to its output.
	Start(ctx context.Context, cmd Command) (Process, error)
}

// ExitError reports a command that ran but exited unsuccessfully.
type ExitError struct {
	// Command is the log-safe description of the invocation.
	Command string
	// Code is the process exit code, or -1 when the process was signalled.
	Code int32
	// Stderr is the (bounded, already truncated) standard error output.
	Stderr string
	// Err carries an underlying cause such as context cancellation.
	Err error
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("%s: exited with code %d", e.Command, e.Code)
	if cause := e.Err; cause != nil {
		msg += ": " + cause.Error()
	}
	if s := strings.TrimSpace(e.Stderr); s != "" {
		msg += ": " + FirstLine(s)
	}
	return msg
}

// Unwrap exposes the underlying cause so errors.Is/errors.As keep working.
func (e *ExitError) Unwrap() error { return e.Err }

// ErrBinaryNotFound indicates that the client binary is not available in the
// execution context.
var ErrBinaryNotFound = errors.New("client binary not found")

// FirstLine returns the first non-empty line of s, truncated to 400 bytes.
// It is used to keep multi-line tool output out of log messages and API errors.
func FirstLine(s string) string {
	for line := range strings.Lines(s) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > maxMessageLen {
			line = line[:maxMessageLen] + "..."
		}
		return line
	}
	return ""
}

// SalientLine returns the most meaningful line of a tool's output: the first
// line mentioning an error, otherwise the last non-empty line, otherwise the
// first one. Verbose tools such as pg_dump print progress before the failure,
// so the first line is usually noise.
func SalientLine(s string) string {
	var first, last, errLine string
	for line := range strings.Lines(s) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if first == "" {
			first = line
		}
		last = line
		if errLine == "" && strings.Contains(strings.ToLower(line), "error") {
			errLine = line
		}
	}
	switch {
	case errLine != "":
		return truncate(errLine)
	case last != "":
		return truncate(last)
	default:
		return ""
	}
}

func truncate(line string) string {
	if len(line) > maxMessageLen {
		return line[:maxMessageLen] + "..."
	}
	return line
}

const maxMessageLen = 400
