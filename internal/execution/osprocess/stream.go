// Package osprocess contains the small amount of OS-level plumbing shared by
// the local and docker execution backends: process spawning, process-group
// management, bounded stderr capture and reliable cleanup.
package osprocess

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// maxStderrBytes bounds the amount of diagnostic output kept in memory so a
// misbehaving command cannot grow the process heap without limit.
const maxStderrBytes = 64 << 10

// pipeGracePeriod bounds how long Wait drains stdout before killing the child.
const pipeGracePeriod = 5 * time.Second

// Spec describes a command to execute.
type Spec struct {
	Path  string
	Args  []string
	Env   []string
	Stdin string
}

// Outcome is the result of a completed execution.
type Outcome struct {
	Stdout []byte
	Stderr string
}

// StartError reports a command that could not be spawned at all.
type StartError struct {
	Spec Spec
	Err  error
}

func (e *StartError) Error() string {
	return fmt.Sprintf("start %s: %v", e.Spec.Path, e.Err)
}

// Unwrap allows errors.Is(err, exec.ErrNotFound) checks.
func (e *StartError) Unwrap() error { return e.Err }

// Start spawns the command without a shell and returns a stream handle.
//
// The child runs in its own process group, so Kill terminates it together with
// any helper process it spawned. Secrets never reach argv; they are only
// present in spec.Env.
func Start(ctx context.Context, spec Spec) (*Stream, error) {
	cmd := exec.CommandContext(ctx, spec.Path, spec.Args...) //nolint:gosec // argv is built from validated values, never through a shell.
	cmd.Env = append(cmd.Environ(), spec.Env...)
	configureCommand(cmd) // own process group
	// Context cancellation must take down the whole group, not just the
	// direct child (docker CLI, shell wrappers, ...).
	cmd.Cancel = func() error {
		terminate(cmd)
		return nil
	}

	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, &StartError{Spec: spec, Err: err}
	}

	stream := &Stream{
		cmd:  cmd,
		out:  stdout,
		done: make(chan struct{}),
	}
	go stream.consumeStderr(stderr)
	return stream, nil
}

// Run executes a command to completion, capturing stdout and stderr.
func Run(ctx context.Context, spec Spec) (Outcome, error) {
	stream, err := Start(ctx, spec)
	if err != nil {
		return Outcome{}, err
	}

	var buf strings.Builder
	_, copyErr := io.Copy(&buf, stream)
	waitErr := stream.Wait()

	outcome := Outcome{Stdout: []byte(buf.String()), Stderr: stream.Stderr()}
	if waitErr != nil {
		return outcome, waitErr
	}
	if copyErr != nil {
		return outcome, fmt.Errorf("read stdout: %w", copyErr)
	}
	return outcome, nil
}

// Stream is a running command with a readable stdout.
type Stream struct {
	cmd      *exec.Cmd
	out      io.ReadCloser
	done     chan struct{}
	waitOnce sync.Once
	waitErr  error

	readMu sync.Mutex

	stderrMu sync.Mutex
	stderr   strings.Builder
	truncMed bool

	killOnce sync.Once
}

// Read implements io.Reader over the child's stdout.
func (s *Stream) Read(p []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	return s.out.Read(p) //nolint:wrapcheck // pass-through of the pipe read.
}

// Stderr returns the diagnostic output captured so far.
func (s *Stream) Stderr() string {
	s.stderrMu.Lock()
	defer s.stderrMu.Unlock()
	out := s.stderr.String()
	if s.truncMed {
		out += "\n[stderr truncated]"
	}
	return out
}

// Kill terminates the process group. It is safe to call from any goroutine and
// more than once.
func (s *Stream) Kill() {
	s.killOnce.Do(func() { terminate(s.cmd) })
}

// Wait reaps the child and releases every pipe. It is safe to call more than
// once. Callers should either drain the stream first or call Kill; Wait drains
// any remaining stdout itself so the child can never deadlock on a full pipe.
func (s *Stream) Wait() error {
	s.waitOnce.Do(func() {
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			s.readMu.Lock()
			defer s.readMu.Unlock()
			_, _ = io.Copy(io.Discard, s.out)
		}()

		select {
		case <-drained:
		case <-time.After(pipeGracePeriod):
			s.Kill()
			<-drained
		}

		<-s.done // stderr consumer finished.
		s.waitErr = s.cmd.Wait()
	})
	return s.waitErr
}

// Pid returns the process id, or 0 before start.
func (s *Stream) Pid() int {
	if s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

func (s *Stream) consumeStderr(r io.ReadCloser) {
	defer close(s.done)
	defer func() { _ = r.Close() }()

	reader := bufio.NewReader(r)
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			s.appendStderr(buf[:n])
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
				s.appendStderr([]byte("\n" + err.Error()))
			}
			return
		}
	}
}

func (s *Stream) appendStderr(chunk []byte) {
	s.stderrMu.Lock()
	defer s.stderrMu.Unlock()

	remaining := maxStderrBytes - s.stderr.Len()
	if remaining <= 0 {
		s.truncMed = true
		return
	}
	if len(chunk) > remaining {
		chunk = chunk[:remaining]
		s.truncMed = true
	}
	s.stderr.Write(chunk)
}
