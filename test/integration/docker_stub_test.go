//go:build unix

package integration_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fmotalleb/pg-dump-dashboard/internal/dump"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/docker"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/local"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/pgclient"
	"github.com/fmotalleb/pg-dump-dashboard/internal/postgres"
)

// dockerStub is a minimal stand-in for the Docker CLI. It understands exactly
// what the dashboard emits: `exec [--interactive] [--env NAME]… [--user U]
// CONTAINER CMD…`, resolves name-only --env entries from its own environment
// (as the real CLI does) and then executes CMD.
//
// This lets the Docker backend be exercised end-to-end — argv layout, secret
// forwarding, streaming, exit codes — without a Docker daemon.
const dockerStub = `#!/bin/sh
set -eu
shift # "exec"
while [ $# -gt 0 ]; do
  case "$1" in
    --interactive|-i) shift ;;
    --env|-e) export "${2%%=*}"; shift 2 ;;
    --user|-u) shift 2 ;;
    *) break ;;
  esac
done
shift # container name
exec "$@"
`

func dockerRunner(t *testing.T) (execution.Runner, string) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "docker")
	if err := os.WriteFile(path, []byte(dockerStub), 0o755); err != nil { //nolint:gosec // test stub must be executable.
		t.Fatalf("write docker stub: %v", err)
	}

	runner, err := docker.NewRunner(local.New(), docker.Config{
		Container:  "pg-primary",
		DockerPath: path,
	})
	if err != nil {
		t.Fatalf("docker runner: %v", err)
	}
	return runner, path
}

func TestDockerBackendDiscoveryThroughStub(t *testing.T) {
	e := env(t)
	runner, stub := dockerRunner(t)

	if runner.Name() != "docker" {
		t.Fatalf("runner name = %q", runner.Name())
	}

	discoverer := &postgres.Discoverer{
		Runner:         runner,
		Binaries:       pgclient.DefaultBinaries(),
		ConnectTimeout: 10 * time.Second,
		QueryTimeout:   20 * time.Second,
		Timeout:        60 * time.Second,
		Concurrency:    2,
	}

	overview, err := discoverer.Overview(context.Background(), e.conn)
	if err != nil {
		t.Fatalf("docker discovery: %v", err)
	}
	if overview.ExecutionContext != "docker" {
		t.Errorf("execution context = %q", overview.ExecutionContext)
	}
	if overview.Totals.Reachable == 0 {
		t.Errorf("no database was reachable through the docker backend")
	}
	if strings.Contains(overview.Endpoint, "secret") {
		t.Errorf("endpoint leaked a credential: %q", overview.Endpoint)
	}

	t.Logf("docker stub used: %s", stub)
}

func TestDockerBackendDumpThroughStub(t *testing.T) {
	e := env(t)
	runner, _ := dockerRunner(t)

	service := &dump.Service{
		Runner:         runner,
		Binaries:       pgclient.DefaultBinaries(),
		ConnectTimeout: 10 * time.Second,
	}

	recorder := httptest.NewRecorder()
	result, err := service.Stream(context.Background(), recorder, e.conn, dump.Request{
		Database: e.conn.Database,
		Format:   pgclient.FormatCustom,
	})
	if err != nil {
		t.Fatalf("docker dump: %v", err)
	}
	if result.Bytes == 0 {
		t.Fatal("the docker backend streamed no bytes")
	}
	if !strings.HasPrefix(recorder.Body.String(), "PGDMP") {
		t.Fatalf("unexpected archive prefix: %.20q", recorder.Body.String())
	}
}

func TestDockerBackendReportsMissingContainerBinary(t *testing.T) {
	e := env(t)
	runner, _ := dockerRunner(t)

	// Point the client binaries at names that cannot exist inside the target.
	discoverer := &postgres.Discoverer{
		Runner:       runner,
		Binaries:     pgclient.Binaries{Psql: "psql-does-not-exist", PgDump: "pg_dump-does-not-exist"},
		QueryTimeout: 10 * time.Second,
		Timeout:      30 * time.Second,
	}

	_, err := discoverer.Overview(context.Background(), e.conn)
	if err == nil {
		t.Fatal("expected an error for a missing client binary")
	}
	if kind := postgres.KindOf(err); kind != postgres.KindClientMissing {
		t.Fatalf("kind = %v (%v)", kind, err)
	}
}
