// Package fake provides an execution.Runner implementation for tests.
//
// It records every command it is asked to run and answers from a scripted
// queue, which lets execution backends and the HTTP layer be tested without a
// real Docker daemon, PostgreSQL server or shell.
package fake

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
)

// Response is a scripted command outcome.
type Response struct {
	Stdout string
	Stderr string
	Err    error
	// Stream, when set, is served to callers of Start instead of Stdout.
	Stream string
}

// Runner records calls and replays scripted responses.
type Runner struct {
	mu        sync.Mutex
	commands  []execution.Command
	responses []Response
	// Started collects commands passed to Start.
	Started []execution.Command
	// StartErr, when set, makes Start fail deterministically.
	StartErr error
	// RunErr, when set, makes every Run fail with that error.
	RunErr error
	// Separate cycling scripts for Run and Start, used by NewScripted.
	runScript   cyclingScript
	startScript cyclingScript
}

// New returns a runner that answers with the given responses in order. When
// the script is exhausted it answers with empty success.
func New(responses ...Response) *Runner {
	return &Runner{responses: responses}
}

// NewScripted returns a runner with independent, infinitely cycling scripts
// for Run and Start. It models a server where metadata queries and dump streams
// are interleaved in an unknown order (connect, refresh, download).
func NewScripted(runs, starts []Response) *Runner {
	return &Runner{
		runScript:   cyclingScript{responses: runs},
		startScript: cyclingScript{responses: starts},
	}
}

// cyclingScript replays its responses in order and wraps around.
type cyclingScript struct {
	responses []Response
	index     int
}

func (c *cyclingScript) next() Response {
	if len(c.responses) == 0 {
		return Response{}
	}
	resp := c.responses[c.index%len(c.responses)]
	c.index++
	return resp
}

// Name implements execution.Runner.
func (*Runner) Name() string { return "fake" }

// Commands returns a copy of every command executed with Run.
func (r *Runner) Commands() []execution.Command {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]execution.Command(nil), r.commands...)
}

// LastCommand returns the most recently executed command.
func (r *Runner) LastCommand() execution.Command {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.commands) == 0 {
		return execution.Command{}
	}
	return r.commands[len(r.commands)-1]
}

// Run implements execution.Runner.
func (r *Runner) Run(_ context.Context, cmd execution.Command) (execution.Result, error) {
	r.mu.Lock()
	r.commands = append(r.commands, cmd)
	runErr := r.RunErr
	resp := r.next()
	if len(r.runScript.responses) > 0 {
		resp = r.runScript.next()
	}
	r.mu.Unlock()

	if runErr != nil {
		return execution.Result{Stdout: []byte(resp.Stdout), Stderr: []byte(resp.Stderr)}, runErr
	}
	return execution.Result{Stdout: []byte(resp.Stdout), Stderr: []byte(resp.Stderr)}, resp.Err
}

// Start implements execution.Runner.
func (r *Runner) Start(_ context.Context, cmd execution.Command) (execution.Process, error) {
	r.mu.Lock()
	r.commands = append(r.commands, cmd)
	r.Started = append(r.Started, cmd)
	resp := r.next()
	if len(r.startScript.responses) > 0 {
		resp = r.startScript.next()
	}
	r.mu.Unlock()

	if r.StartErr != nil {
		return nil, r.StartErr
	}
	if resp.Err != nil {
		return nil, resp.Err
	}
	out := resp.Stream
	if out == "" {
		out = resp.Stdout
	}
	return &Process{Reader: strings.NewReader(out), stderr: resp.Stderr}, nil
}

func (r *Runner) next() Response {
	if len(r.responses) == 0 {
		return Response{}
	}
	resp := r.responses[0]
	r.responses = r.responses[1:]
	return resp
}

// Process is a scripted execution.Process.
type Process struct {
	io.Reader

	mu      sync.Mutex
	stderr  string
	killed  bool
	waitErr error
	Waited  chan struct{}
}

// Kill marks the process as terminated.
func (p *Process) Kill() {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
}

// Killed reports whether Kill was called.
func (p *Process) Killed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed
}

// Stderr implements execution.Process.
func (p *Process) Stderr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stderr
}

// Wait implements execution.Process.
func (p *Process) Wait() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.killed && p.waitErr == nil {
		p.waitErr = &execution.ExitError{Command: "fake", Code: -1, Err: errors.New("killed")}
	}
	return p.waitErr
}

// BlockingRunner is a runner whose streaming processes block until their
// context is cancelled; it models a long running pg_dump.
type BlockingRunner struct {
	mu      sync.Mutex
	started []execution.Command
	KillCh  chan struct{}
	// Emit is written to the process stdout before blocking.
	Emit string
}

// NewBlocking returns a BlockingRunner.
func NewBlocking(emit string) *BlockingRunner {
	return &BlockingRunner{KillCh: make(chan struct{}), Emit: emit}
}

// Name implements execution.Runner.
func (*BlockingRunner) Name() string { return "fake-blocking" }

// Run implements execution.Runner.
func (r *BlockingRunner) Run(context.Context, execution.Command) (execution.Result, error) {
	return execution.Result{}, errors.New("BlockingRunner only supports Start")
}

// Commands returns the commands passed to Start.
func (r *BlockingRunner) Commands() []execution.Command {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]execution.Command(nil), r.started...)
}

// Start implements execution.Runner.
func (r *BlockingRunner) Start(ctx context.Context, cmd execution.Command) (execution.Process, error) {
	r.mu.Lock()
	r.started = append(r.started, cmd)
	r.mu.Unlock()

	pr, pw := io.Pipe()
	go func() {
		if r.Emit != "" {
			_, _ = io.WriteString(pw, r.Emit)
		}
		select {
		case <-ctx.Done():
		case <-r.KillCh:
		}
		_ = pw.CloseWithError(io.EOF)
	}()

	return &Process{Reader: pr, Waited: make(chan struct{})}, nil
}

// BytesBuffer returns a reader over s; helper for tests.
func BytesBuffer(s string) io.Reader { return bytes.NewReader([]byte(s)) }
