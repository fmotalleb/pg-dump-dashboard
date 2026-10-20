package app_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fmotalleb/pg-dump-dashboard/internal/app"
	"github.com/fmotalleb/pg-dump-dashboard/internal/config"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/pgclient"
)

func testLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestNewSelectsLocalBackend(t *testing.T) {
	t.Parallel()

	application, err := app.New(config.Defaults(), testLogger())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	if got := application.Runner().Name(); got != "local" {
		t.Fatalf("runner = %q, want local", got)
	}
}

func TestNewSelectsDockerBackend(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Execution.Mode = config.ExecutionDocker
	cfg.Execution.DockerTarget.Container = "pg-primary"

	application, err := app.New(cfg, testLogger())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	if got := application.Runner().Name(); got != "docker" {
		t.Fatalf("runner = %q, want docker", got)
	}
}

func TestNewRejectsDockerWithoutContainer(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Execution.Mode = config.ExecutionDocker

	if _, err := app.New(cfg, testLogger()); err == nil {
		t.Fatal("a docker backend without an explicit container must be rejected")
	}
}

func TestHandlerServesAPIAndFrontend(t *testing.T) {
	t.Parallel()

	application, err := app.New(config.Defaults(), testLogger())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}

	recorder := httptest.NewRecorder()
	application.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", recorder.Code)
	}

	meta := httptest.NewRecorder()
	application.Handler().ServeHTTP(meta, httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil))
	if meta.Code != http.StatusOK || !strings.Contains(meta.Body.String(), "executionContext") {
		t.Fatalf("meta: %d %s", meta.Code, meta.Body.String())
	}
}

func TestRunFailsOnBusyPort(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Listen = "127.0.0.1:0"

	application, err := app.New(cfg, testLogger())
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// With a cancelled context the server starts and shuts down immediately.
	if err := application.Run(ctx); err != nil {
		t.Fatalf("run with cancelled context: %v", err)
	}
}

func TestBaseConnectionHasNoCredentials(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Postgres.Host = "db.internal"
	cfg.Postgres.Port = 6543

	conn := pgclient.Conn{Host: cfg.Postgres.Host, Port: cfg.Postgres.Port}
	for _, entry := range conn.Env() {
		if strings.Contains(entry, "PGPASSWORD") {
			t.Fatalf("the base connection must not carry a password: %s", entry)
		}
	}
}
