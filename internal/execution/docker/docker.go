// Package docker executes PostgreSQL client commands inside an explicitly
// configured Docker container.
//
// The container is never discovered automatically: the operator must name it,
// because guessing would silently target the wrong database.
//
// Security note: the Docker CLI talks to the Docker daemon through its socket.
// Access to that socket is equivalent to root on the Docker host. See the
// README ("Security") before enabling this backend.
package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
)

// Config describes the Docker execution target.
type Config struct {
	// Container is the explicit target container name or id.
	Container string
	// DockerPath is the Docker CLI executable (default "docker").
	DockerPath string
	// PgUser optionally runs the command as a specific user inside the
	// container (docker exec --user).
	PgUser string
}

// Validate reports configuration problems early, at startup.
func (c Config) Validate() error {
	if err := execution.ValidContainerName(c.Container); err != nil {
		return err
	}
	if c.DockerPath == "" {
		return fmt.Errorf("docker executable path must not be empty")
	}
	return nil
}

// Translator converts a command into "docker exec <container> ...".
type Translator struct {
	Config Config
}

// Translate implements execution.Translator.
//
// Connection parameters travel as "docker exec -e KEY=VALUE" entries, so they
// are not visible in the argv of the spawned docker process (argv can be read
// by any local user through /proc) and are not expanded by a shell.
func (t Translator) Translate(cmd execution.Command) (execution.Command, error) {
	if err := t.Config.Validate(); err != nil {
		return execution.Command{}, err
	}

	args := make([]string, 0, len(cmd.Args)+len(cmd.Env)*2+8)
	args = append(args, "exec")
	if cmd.Stdin != "" {
		args = append(args, "--interactive")
	}
	for _, entry := range cmd.Env {
		args = append(args, "--env", entry)
	}
	if t.Config.PgUser != "" {
		args = append(args, "--user", t.Config.PgUser)
	}
	args = append(args, t.Config.Container, cmd.Path)
	args = append(args, cmd.Args...)

	return execution.Command{
		Path:  t.Config.DockerPath,
		Args:  args,
		Stdin: cmd.Stdin,
		// Env intentionally empty: the docker CLI itself needs no database
		// credentials and must not inherit them.
	}, nil
}

// Runner executes commands inside the container by translating them and
// delegating to a base runner (normally the local one).
//
// Splitting the work this way keeps the backend testable without a Docker
// daemon: tests supply a recording base runner.
type Runner struct {
	base       execution.Runner
	translator execution.Translator
}

// NewRunner returns a Docker backend that spawns the Docker CLI through base.
func NewRunner(base execution.Runner, cfg Config) (*Runner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Runner{base: base, translator: Translator{Config: cfg}}, nil
}

// NewRunnerWithTranslator allows tests to inject a recording runner.
func NewRunnerWithTranslator(base execution.Runner, t execution.Translator) *Runner {
	return &Runner{base: base, translator: t}
}

// Name implements execution.Runner.
func (*Runner) Name() string { return "docker" }

// Run implements execution.Runner.
func (r *Runner) Run(ctx context.Context, cmd execution.Command) (execution.Result, error) {
	translated, err := r.translator.Translate(cmd)
	if err != nil {
		return execution.Result{}, fmt.Errorf("translate command: %w", err)
	}
	return r.base.Run(ctx, translated) //nolint:wrapcheck // backend errors are already typed.
}

// Start implements execution.Runner.
func (r *Runner) Start(ctx context.Context, cmd execution.Command) (execution.Process, error) {
	translated, err := r.translator.Translate(cmd)
	if err != nil {
		return nil, fmt.Errorf("translate command: %w", err)
	}
	proc, err := r.base.Start(ctx, translated)
	if err != nil {
		return nil, fmt.Errorf("start container command: %w", err)
	}
	return proc, nil
}

// Describe returns a log-safe rendering of the target.
func (c Config) Describe() string {
	return strings.Join([]string{"docker", "container=" + c.Container, "user=" + c.PgUser}, " ")
}
