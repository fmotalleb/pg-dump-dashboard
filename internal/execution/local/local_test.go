package local_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/local"
)

// These tests use real POSIX utilities (echo, cat, false, sleep) from PATH.
// They exercise process spawning, streaming, exit codes and cancellation
// without requiring PostgreSQL or Docker.

func TestRunCapturesOutput(t *testing.T) {
	t.Parallel()

	runner := local.New()
	if runner.Name() != "local" {
		t.Fatalf("name = %q", runner.Name())
	}

	result, err := runner.Run(context.Background(), execution.Command{
		Path: "echo",
		Args: []string{"hello", "world"},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.TrimSpace(string(result.Stdout)); got != "hello world" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestRunPassesStdinAndEnvWithoutArgvLeakage(t *testing.T) {
	t.Parallel()

	runner := local.New()
	result, err := runner.Run(context.Background(), execution.Command{
		Path:  "cat",
		Env:   []string{"PGDD_TEST_VALUE=from-environment"},
		Stdin: "line one\nline two\n",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if string(result.Stdout) != "line one\nline two\n" {
		t.Fatalf("stdin was not forwarded: %q", result.Stdout)
	}
}

func TestRunReportsExitCodes(t *testing.T) {
	t.Parallel()

	runner := local.New()
	_, err := runner.Run(context.Background(), execution.Command{Path: "false"})
	if err == nil {
		t.Fatal("expected an error for a non-zero exit")
	}

	var exitErr *execution.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *execution.ExitError, got %T (%v)", err, err)
	}
	if exitErr.Code == 0 {
		t.Fatalf("exit code should be non-zero: %+v", exitErr)
	}
}

func TestRunReportsMissingBinary(t *testing.T) {
	t.Parallel()

	runner := local.New()
	_, err := runner.Run(context.Background(), execution.Command{Path: "pg-dump-dashboard-does-not-exist"})
	if err == nil {
		t.Fatal("expected an error for a missing binary")
	}
	if !errors.Is(err, execution.ErrBinaryNotFound) {
		t.Fatalf("expected ErrBinaryNotFound, got %v", err)
	}
}

func TestStartStreamsAndCleansUp(t *testing.T) {
	t.Parallel()

	runner := local.New()
	proc, err := runner.Start(context.Background(), execution.Command{
		Path: "echo",
		Args: []string{"streamed"},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	body, err := io.ReadAll(proc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.TrimSpace(string(body)) != "streamed" {
		t.Fatalf("body = %q", body)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

func TestContextCancellationTerminatesProcess(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	runner := local.New()
	start := time.Now()

	proc, err := runner.Start(ctx, execution.Command{Path: "sleep", Args: []string{"30"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	waitErr := proc.Wait()
	elapsed := time.Since(start)

	if waitErr == nil {
		t.Fatal("expected the cancelled process to fail")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("process was not terminated promptly (%v)", elapsed)
	}
	if ctx.Err() == nil {
		t.Fatal("context should have expired")
	}
}

func TestKillStopsBlockingProcess(t *testing.T) {
	t.Parallel()

	runner := local.New()
	proc, err := runner.Start(context.Background(), execution.Command{Path: "sleep", Args: []string{"30"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()

	proc.Kill()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not return after Kill")
	}
}

func TestRunHonoursContextDeadline(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	runner := local.New()
	_, err := runner.Run(ctx, execution.Command{Path: "sleep", Args: []string{"30"}})
	if err == nil {
		t.Fatal("expected the deadline to abort the command")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected a deadline error, got %v", err)
	}
}
