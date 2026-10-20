package dump_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fmotalleb/pg-dump-dashboard/internal/dump"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/fake"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/pgclient"
)

func newService(runner execution.Runner) *dump.Service {
	return &dump.Service{
		Runner:   runner,
		Binaries: pgclient.DefaultBinaries(),
	}
}

func testConn() pgclient.Conn {
	return pgclient.Conn{
		Host:     "db",
		Port:     5432,
		User:     "reporter",
		Password: execution.Secret("very-secret"),
		Database: "appdb",
	}
}

func TestStreamWritesAttachmentHeaders(t *testing.T) {
	t.Parallel()

	runner := fake.New(fake.Response{Stream: "PGDMP-fake-archive"})
	service := newService(runner)

	recorder := httptest.NewRecorder()
	result, err := service.Stream(context.Background(), recorder, testConn(), dump.Request{
		Database: "appdb",
		Format:   pgclient.FormatCustom,
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	header := recorder.Header()
	if got := header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("content type = %q", got)
	}
	disposition := header.Get("Content-Disposition")
	if !strings.HasPrefix(disposition, "attachment;") || !strings.Contains(disposition, "appdb-") {
		t.Errorf("content disposition = %q", disposition)
	}
	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff header missing: %q", got)
	}
	if recorder.Body.String() != "PGDMP-fake-archive" {
		t.Errorf("body = %q", recorder.Body.String())
	}
	if result.Bytes != int64(len("PGDMP-fake-archive")) || !result.Started {
		t.Errorf("unexpected result: %+v", result)
	}

	// The dump must be executed with the validated database name and without
	// the password on the command line.
	cmd := runner.LastCommand()
	if cmd.Path != "pg_dump" {
		t.Fatalf("executed %q", cmd.Path)
	}
	if strings.Contains(cmd.Describe(), "very-secret") {
		t.Fatalf("password leaked into argv: %s", cmd.Describe())
	}
	if !strings.Contains(cmd.Describe(), "-- appdb") {
		t.Fatalf("database not passed as the final argument: %s", cmd.Describe())
	}
}

func TestStreamReportsFailureBeforeFirstByte(t *testing.T) {
	t.Parallel()

	runner := fake.New()
	runner.StartErr = &execution.ExitError{
		Command: "pg_dump",
		Code:    1,
		Stderr:  `pg_dump: error: connection to server failed: FATAL: password authentication failed for user "reporter"`,
	}
	service := newService(runner)

	recorder := httptest.NewRecorder()
	_, err := service.Stream(context.Background(), recorder, testConn(), dump.Request{Database: "appdb"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if recorder.Code != http.StatusOK {
		t.Fatal("no response should be written when the dump never started")
	}
	if strings.Contains(err.Error(), "very-secret") {
		t.Fatalf("error leaked the password: %v", err)
	}
}

func TestStreamClassifiesMidStreamFailure(t *testing.T) {
	t.Parallel()

	// A process that emits a chunk and then fails models pg_dump dying
	// mid-stream.
	failing := &failingRunner{chunks: []string{"partial-data"}, err: &execution.ExitError{
		Command: "pg_dump",
		Code:    1,
		Stderr:  "pg_dump: error: server closed the connection unexpectedly",
	}}
	service := newService(failing)

	recorder := httptest.NewRecorder()
	result, err := service.Stream(context.Background(), recorder, testConn(), dump.Request{Database: "appdb"})
	if err == nil {
		t.Fatal("expected an error for a truncated dump")
	}
	if !result.Started {
		t.Fatal("streaming had already started, so Started must be true")
	}
	if recorder.Body.String() != "partial-data" {
		t.Fatalf("body = %q", recorder.Body.String())
	}
	if !strings.Contains(err.Error(), "server closed the connection") {
		t.Fatalf("expected the tool diagnostic in the error: %v", err)
	}
}

func TestStreamStopsWhenClientDisconnects(t *testing.T) {
	t.Parallel()

	blocking := fake.NewBlocking("some-bytes")
	service := newService(blocking)

	writer := &disconnectingWriter{failAfter: 1}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := service.Stream(ctx, writer, testConn(), dump.Request{Database: "appdb"})
	if err == nil {
		t.Fatal("expected the cancelled stream to fail")
	}
	if !errors.Is(err, dump.ErrClientGone) && !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStreamRejectsUnsafeDatabaseName(t *testing.T) {
	t.Parallel()

	service := newService(fake.New())
	recorder := httptest.NewRecorder()

	_, err := service.Stream(context.Background(), recorder, testConn(), dump.Request{
		Database: "../../etc/passwd",
	})
	if err == nil {
		t.Fatal("expected an error for an unsafe database name")
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("nothing should be written: %q", recorder.Body.String())
	}
}

func TestStreamHonoursMaxDuration(t *testing.T) {
	t.Parallel()

	service := newService(fake.NewBlocking("x"))
	service.MaxDuration = 120 * time.Millisecond

	start := time.Now()
	_, err := service.Stream(context.Background(), httptest.NewRecorder(), testConn(), dump.Request{Database: "appdb"})
	if err == nil {
		t.Fatal("expected the timeout to abort the dump")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("dump was not aborted promptly: %v", elapsed)
	}
}

// failingRunner streams a couple of chunks and then fails, modelling a
// pg_dump process that dies after producing output.
type failingRunner struct {
	chunks []string
	err    error
}

func (r *failingRunner) Name() string { return "failing" }

func (r *failingRunner) Run(context.Context, execution.Command) (execution.Result, error) {
	return execution.Result{}, errors.New("not supported")
}

func (r *failingRunner) Start(context.Context, execution.Command) (execution.Process, error) {
	var mu sync.Mutex
	reader, writer := io.Pipe()
	go func() {
		mu.Lock()
		defer mu.Unlock()
		for _, chunk := range r.chunks {
			_, _ = io.WriteString(writer, chunk)
		}
		_ = writer.CloseWithError(io.EOF)
	}()
	return &failingProcess{Reader: reader, err: r.err}, nil
}

type failingProcess struct {
	io.Reader
	err error
}

func (p *failingProcess) Kill() {}
func (p *failingProcess) Stderr() string {
	return "pg_dump: error: server closed the connection unexpectedly"
}
func (p *failingProcess) Wait() error { return p.err }

// disconnectingWriter fails after n successful writes.
type disconnectingWriter struct {
	header    http.Header
	failAfter int
	written   int
	mu        sync.Mutex
}

func (w *disconnectingWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *disconnectingWriter) WriteHeader(int) {}

func (w *disconnectingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.written++
	if w.written > w.failAfter {
		return 0, errors.New("client disconnected")
	}
	return len(p), nil
}
