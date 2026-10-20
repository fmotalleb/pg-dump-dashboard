// Package app wires configuration, execution backends, discovery, dump
// orchestration and the HTTP server together.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/fmotalleb/pg-dump-dashboard/internal/config"
	"github.com/fmotalleb/pg-dump-dashboard/internal/dump"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/docker"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/local"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/pgclient"
	"github.com/fmotalleb/pg-dump-dashboard/internal/httpapi"
	"github.com/fmotalleb/pg-dump-dashboard/internal/postgres"
	"github.com/fmotalleb/pg-dump-dashboard/internal/session"
	"github.com/fmotalleb/pg-dump-dashboard/internal/version"
	"github.com/fmotalleb/pg-dump-dashboard/internal/web"
)

// App is the assembled application.
type App struct {
	cfg      config.Config
	logger   *slog.Logger
	runner   execution.Runner
	sessions *session.Store
	handler  http.Handler
}

// New assembles the application, validating the execution backend.
func New(cfg config.Config, logger *slog.Logger) (*App, error) {
	runner, err := newRunner(cfg)
	if err != nil {
		return nil, err
	}

	staticFS, err := web.Dist()
	if err != nil {
		return nil, fmt.Errorf("load embedded frontend: %w", err)
	}
	if !web.HasAssets(staticFS) {
		logger.Warn("the compiled frontend is not embedded in this binary; run `make frontend` and rebuild")
	}

	baseConn := pgclient.Conn{
		Host:     cfg.Postgres.Host,
		Port:     cfg.Postgres.Port,
		Database: cfg.Postgres.Database,
		SSLMode:  cfg.Postgres.SSLMode,
	}

	discoverer := &postgres.Discoverer{
		Runner:         runner,
		Binaries:       cfg.Execution.Binaries,
		ConnectTimeout: cfg.ConnectTimeout,
		QueryTimeout:   cfg.QueryTimeout,
		Timeout:        cfg.MetadataTimeout,
		Concurrency:    cfg.MetadataConcurrency,
		MaxDatabases:   cfg.MetadataMaxDatabases,
	}

	dumpService := &dump.Service{
		Runner:         runner,
		Binaries:       cfg.Execution.Binaries,
		ConnectTimeout: cfg.ConnectTimeout,
		MaxDuration:    cfg.DumpTimeout,
		Logger:         logger,
	}

	sessions := session.NewStore(session.DefaultTTL)

	apiHandler := httpapi.New(
		httpapi.Options{
			Endpoint:           cfg.Postgres.Endpoint(),
			ExecutionContext:   runner.Name(),
			Version:            version.Version,
			MaxConcurrentDumps: cfg.MaxConcurrentDumps,
			MaxRequestBytes:    cfg.MaxRequestBodyBytes,
			SessionTTL:         sessions.TTL(),
			CookieSecure:       cfg.CookieSecure,
		},
		baseConn,
		discoverer,
		dumpService,
		sessions,
		logger,
		staticFS,
	)

	return &App{cfg: cfg, logger: logger, runner: runner, sessions: sessions, handler: apiHandler.Routes()}, nil
}

// newRunner builds the configured execution backend. This is the single place
// where the backend is selected; no other package branches on it.
func newRunner(cfg config.Config) (execution.Runner, error) {
	switch cfg.Execution.Mode {
	case config.ExecutionLocal:
		return local.New(), nil
	case config.ExecutionDocker:
		runner, err := docker.NewRunner(local.New(), docker.Config{
			Container:  cfg.Execution.DockerTarget.Container,
			DockerPath: cfg.Execution.DockerTarget.DockerPath,
			PgUser:     cfg.Execution.DockerTarget.PgUser,
		})
		if err != nil {
			return nil, fmt.Errorf("docker execution backend: %w", err)
		}
		return runner, nil
	default:
		return nil, fmt.Errorf("unsupported execution backend %q", cfg.Execution.Mode)
	}
}

// Handler exposes the HTTP handler (used by tests).
func (a *App) Handler() http.Handler { return a.handler }

// Runner exposes the execution backend (used by tests and the banner).
func (a *App) Runner() execution.Runner { return a.runner }

// Run serves HTTP until ctx is cancelled, then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", a.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", a.cfg.Listen, err)
	}

	server := &http.Server{
		Handler:           a.handler,
		ReadHeaderTimeout: a.cfg.ReadHeaderTimeout,
		IdleTimeout:       2 * time.Minute,
		// No ReadTimeout/WriteTimeout: dump downloads legitimately run for a
		// long time and the client controls their lifetime.
		ErrorLog: slog.NewLogLogger(a.logger.Handler(), slog.LevelWarn),
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}

	a.logBanner(listener.Addr())

	gcCtx, stopGC := context.WithCancel(ctx)
	defer stopGC()
	go a.sessions.RunGC(gcCtx, 5*time.Minute)

	serveErr := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err //nolint:wrapcheck // already contextual.
	case <-ctx.Done():
	}

	a.logger.Info("shutting down", slog.Duration("timeout", a.cfg.ShutdownTimeout))

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.cfg.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		// Dump streams may still be running when the shutdown deadline hits.
		a.logger.Warn("graceful shutdown timed out, closing connections", slog.String("error", err.Error()))
		_ = server.Close()
	}
	return <-serveErr
}

func (a *App) logBanner(addr net.Addr) {
	a.logger.Info("pg-dump-dashboard started",
		slog.String("version", version.Version),
		slog.String("listen", addr.String()),
		slog.String("postgres", a.cfg.Postgres.Endpoint()),
		slog.String("execution", a.runner.Name()),
		slog.String("maintenance_database", a.cfg.Postgres.Database),
	)
	if a.cfg.Execution.Mode == config.ExecutionDocker {
		a.logger.Info("docker execution target",
			slog.String("container", a.cfg.Execution.DockerTarget.Container),
			slog.String("docker", a.cfg.Execution.DockerTarget.DockerPath),
		)
	}
	a.logger.Warn("the dashboard has no authentication of its own; bind it to a trusted network or place it behind an authenticating proxy")
}
