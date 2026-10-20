package dump

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/pgclient"
	"github.com/fmotalleb/pg-dump-dashboard/internal/postgres"
)

// ErrClientGone indicates that duplication stopped because the client
// disconnected or cancelled the request.
var ErrClientGone = errors.New("client cancelled the download")

// Service streams pg_dump output. It is immutable and safe for concurrent use.
type Service struct {
	Runner   execution.Runner
	Binaries pgclient.Binaries
	// ConnectTimeout bounds libpq's connection attempt.
	ConnectTimeout time.Duration
	// MaxDuration bounds a single dump. Zero means unlimited (the client
	// controls the lifetime).
	MaxDuration time.Duration
	// Logger receives operational diagnostics. It must never receive
	// credentials.
	Logger *slog.Logger
}

// Request describes one dump.
type Request struct {
	Database string
	Format   pgclient.DumpFormat
	Schema   string
}

// StreamResult reports what happened during a streamed dump.
type StreamResult struct {
	Filename string
	Bytes    int64
	Started  bool
}

func (s *Service) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func (s *Service) dumpOptions(req Request) pgclient.DumpOptions {
	format := req.Format
	if format == "" {
		format = pgclient.FormatCustom
	}
	return pgclient.DumpOptions{Format: format, Schema: req.Schema}
}

// Stream runs pg_dump and copies its standard output to w.
//
// The response body is not buffered: bytes are forwarded as they arrive.
// Nothing is written to w until the process produced its first byte, so a dump
// that fails immediately can still be reported with an accurate HTTP status
// by the caller.
//
// When the client disconnects, ctx is cancelled and the dump process is
// terminated; Stream returns ErrClientGone in that case.
func (s *Service) Stream(ctx context.Context, w http.ResponseWriter, conn pgclient.Conn, req Request) (StreamResult, error) {
	if s.Runner == nil {
		return StreamResult{}, &postgres.Error{Kind: postgres.KindInternal, Message: "no execution backend configured"}
	}
	if err := pgclient.ValidDatabaseName(req.Database); err != nil {
		return StreamResult{}, &postgres.Error{Kind: postgres.KindInternal, Message: err.Error(), Err: err}
	}

	opts := s.dumpOptions(req)
	conn.Database = req.Database
	conn.ConnectTimeout = s.ConnectTimeout

	cmd, err := pgclient.DumpCommand(s.Binaries, conn, opts)
	if err != nil {
		return StreamResult{}, &postgres.Error{Kind: postgres.KindInternal, Message: err.Error(), Err: err}
	}

	if s.MaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.MaxDuration)
		defer cancel()
	}

	proc, err := s.Runner.Start(ctx, cmd)
	if err != nil {
		return StreamResult{}, postgres.Classify("dump "+req.Database, err)
	}
	// Always terminate and reap the child, whatever happens below.
	defer proc.Kill()
	defer func() { _ = proc.Wait() }()

	// Watchdog: terminate the process as soon as the request context is done.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-ctx.Done():
			proc.Kill()
		case <-stop:
		}
	}()
	defer func() { close(stop); wg.Wait() }()

	reader := bufio.NewReaderSize(proc, 64<<10)

	// Wait for the first byte so that an immediate failure (unknown database,
	// bad credentials, missing binary) still produces a proper error response.
	if _, peekErr := reader.Peek(1); peekErr != nil {
		proc.Kill()
		waitErr := proc.Wait()
		if ctx.Err() != nil {
			return StreamResult{}, ErrClientGone
		}
		if waitErr != nil {
			return StreamResult{}, s.classifyDumpError(req.Database, waitErr, proc.Stderr(), cmd.Env)
		}
		return StreamResult{}, &postgres.Error{
			Kind:    postgres.KindInternal,
			Message: fmt.Sprintf("dump %s: pg_dump produced no output", req.Database),
			Err:     peekErr,
		}
	}

	filename := Filename(req.Database, opts.Format, time.Now())

	header := w.Header()
	header.Set("Content-Type", ContentType(opts.Format))
	header.Set("Content-Disposition", ContentDisposition(filename))
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Dump-Database", req.Database)
	header.Set("X-Dump-Format", string(opts.Format))
	w.WriteHeader(http.StatusOK)

	written, copyErr := io.Copy(w, &ctxReader{ctx: ctx, reader: reader})
	s.flush(w)

	result := StreamResult{Filename: filename, Bytes: written, Started: true}

	// Reap the process, then decide whether the transfer was successful.
	waitErr := proc.Wait()

	switch {
	case ctx.Err() != nil:
		return result, ErrClientGone
	case copyErr != nil:
		// Writing to the client failed (most likely a disconnect).
		return result, fmt.Errorf("stream dump: %w", copyErr)
	case waitErr != nil:
		return result, s.classifyDumpError(req.Database, waitErr, proc.Stderr(), cmd.Env)
	default:
		s.logger().Info("dump streamed",
			slog.String("database", req.Database),
			slog.String("format", string(opts.Format)),
			slog.Int64("bytes", written),
		)
		return result, nil
	}
}

// classifyDumpError converts a pg_dump failure into a sanitised, classified
// error. Tool output is redacted and truncated to a single line.
func (s *Service) classifyDumpError(database string, waitErr error, stderr string, env []string) error {
	safe := execution.RedactEnv(stderr, env)
	if safe == "" {
		return postgres.Classify("dump "+database, waitErr)
	}
	wrapped := fmt.Errorf("%s", execution.SalientLine(safe))
	classified := postgres.Classify("dump "+database, wrapped)
	classified.Err = waitErr
	return classified
}

func (s *Service) flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// ctxReader turns context cancellation into a read error so io.Copy stops even
// when the underlying process has not noticed the closure yet.
type ctxReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *ctxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err //nolint:wrapcheck // surfaced as a copy error.
	}
	return r.reader.Read(p) //nolint:wrapcheck // pass-through.
}
