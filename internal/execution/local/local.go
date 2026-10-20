// Package local executes PostgreSQL client commands in the environment the web
// application itself runs in.
package local

import (
	"context"

	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/osprocess"
)

// Runner implements execution.Runner for the local environment.
type Runner struct{}

// New returns a local runner.
func New() *Runner { return &Runner{} }

// Name implements execution.Runner.
func (*Runner) Name() string { return "local" }

// Run implements execution.Runner.
func (*Runner) Run(ctx context.Context, cmd execution.Command) (execution.Result, error) {
	outcome, err := osprocess.Run(ctx, spec(cmd))
	if err != nil {
		return execution.Result{Stdout: outcome.Stdout, Stderr: []byte(outcome.Stderr)},
			execution.CommandError(ctx, cmd, outcome.Stderr, err)
	}
	return execution.Result{
		Stdout: outcome.Stdout,
		Stderr: []byte(outcome.Stderr),
	}, nil
}

// Start implements execution.Runner.
func (*Runner) Start(ctx context.Context, cmd execution.Command) (execution.Process, error) {
	stream, err := osprocess.Start(ctx, spec(cmd))
	if err != nil {
		return nil, execution.CommandError(ctx, cmd, "", err)
	}
	return &process{ctx: ctx, cmd: cmd, stream: stream}, nil
}

func spec(cmd execution.Command) osprocess.Spec {
	return osprocess.Spec{
		Path:  cmd.Path,
		Args:  cmd.Args,
		Env:   cmd.Env,
		Stdin: cmd.Stdin,
	}
}

type process struct {
	ctx    context.Context
	cmd    execution.Command
	stream *osprocess.Stream
}

func (p *process) Read(b []byte) (int, error) { return p.stream.Read(b) } //nolint:wrapcheck // stream pass-through.

func (p *process) Kill() { p.stream.Kill() }

func (p *process) Stderr() string {
	return execution.RedactEnv(p.stream.Stderr(), p.cmd.Env)
}

func (p *process) Wait() error {
	return execution.CommandError(p.ctx, p.cmd, p.stream.Stderr(), p.stream.Wait()) //nolint:wrapcheck // mapping layer.
}
