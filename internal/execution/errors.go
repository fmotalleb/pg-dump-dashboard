package execution

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// secretEnvMarkers are substrings that mark an environment variable as
// sensitive; their values are scrubbed from any text produced by a tool.
var secretEnvMarkers = []string{"PASSWORD", "SECRET", "TOKEN", "PASSFILE"}

// RedactEnv removes the values of sensitive environment variables from msg.
// It is used on stderr and error text before it is logged or returned to a
// client.
func RedactEnv(msg string, env []string) string {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || len(value) < 3 {
			continue
		}
		upper := strings.ToUpper(key)
		for _, marker := range secretEnvMarkers {
			if strings.Contains(upper, marker) {
				msg = strings.ReplaceAll(msg, value, Redacted)
				break
			}
		}
	}
	return msg
}

// CommandError converts a failure returned by an execution backend into a
// typed error suitable for logs and API responses.
//
// The produced error never contains environment values; any occurrence of a
// secret is redacted.
func CommandError(ctx context.Context, cmd Command, stderr string, err error) error {
	if err == nil {
		return nil
	}
	stderr = RedactEnv(stderr, cmd.Env)

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		typed := &ExitError{
			Command: cmd.Describe(),
			Code:    int32(exitErr.ExitCode()), //nolint:gosec // exit codes are small.
			Stderr:  stderr,
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			typed.Err = ctxErr
		}
		return typed
	}

	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, exec.ErrWaitDelay) {
		return fmt.Errorf("%w: %q is not available in this execution context: %w",
			ErrBinaryNotFound, cmd.Path, err)
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s: %w: %w", cmd.Describe(), ctxErr, err)
	}
	return fmt.Errorf("%s: %w", cmd.Describe(), err)
}
