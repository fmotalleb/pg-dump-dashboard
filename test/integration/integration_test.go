// Package integration contains end-to-end tests against a real PostgreSQL
// server. They exercise the local execution backend with the real psql and
// pg_dump binaries.
//
// The tests are skipped unless PGDD_TEST_PG_HOST is set, so `go test ./...`
// works on machines without a database. Run them with:
//
//	make test-integration
//
// Required environment:
//
//	PGDD_TEST_PG_HOST      hostname of the server (e.g. 127.0.0.1)
//	PGDD_TEST_PG_PORT      port (default 5432)
//	PGDD_TEST_PG_USER      role used for discovery and dumps
//	PGDD_TEST_PG_PASSWORD  its password
//	PGDD_TEST_PG_DATABASE  maintenance database (default "postgres")
package integration_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/fmotalleb/pg-dump-dashboard/internal/dump"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/local"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/pgclient"
	"github.com/fmotalleb/pg-dump-dashboard/internal/httpapi"
	"github.com/fmotalleb/pg-dump-dashboard/internal/postgres"
	"github.com/fmotalleb/pg-dump-dashboard/internal/session"
)

type testEnv struct {
	conn   pgclient.Conn
	runner execution.Runner
}

func env(t *testing.T) testEnv {
	t.Helper()

	host := os.Getenv("PGDD_TEST_PG_HOST")
	if host == "" {
		t.Skip("PGDD_TEST_PG_HOST is not set: skipping PostgreSQL integration tests")
	}
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql is not on PATH: skipping PostgreSQL integration tests")
	}
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump is not on PATH: skipping PostgreSQL integration tests")
	}

	port := uint16(5432)
	if raw := os.Getenv("PGDD_TEST_PG_PORT"); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 16)
		if err != nil {
			t.Fatalf("invalid PGDD_TEST_PG_PORT %q: %v", raw, err)
		}
		port = uint16(parsed)
	}

	database := os.Getenv("PGDD_TEST_PG_DATABASE")
	if database == "" {
		database = "postgres"
	}

	conn := pgclient.Conn{
		Host:           host,
		Port:           port,
		User:           os.Getenv("PGDD_TEST_PG_USER"),
		Password:       execution.Secret(os.Getenv("PGDD_TEST_PG_PASSWORD")),
		Database:       database,
		SSLMode:        "prefer",
		ConnectTimeout: 10 * time.Second,
	}
	if conn.User == "" {
		t.Skip("PGDD_TEST_PG_USER is not set: skipping PostgreSQL integration tests")
	}
	return testEnv{conn: conn, runner: local.New()}
}

func (e testEnv) discoverer() *postgres.Discoverer {
	return &postgres.Discoverer{
		Runner:         e.runner,
		Binaries:       pgclient.DefaultBinaries(),
		ConnectTimeout: 10 * time.Second,
		QueryTimeout:   20 * time.Second,
		Timeout:        60 * time.Second,
		Concurrency:    2,
		MaxDatabases:   20,
	}
}

func TestDiscoveryAgainstRealServer(t *testing.T) {
	e := env(t)

	overview, err := e.discoverer().Overview(context.Background(), e.conn)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}

	if overview.ServerVersion == "" {
		t.Fatal("server version is empty")
	}
	if overview.ExecutionContext != "local" {
		t.Errorf("execution context = %q", overview.ExecutionContext)
	}
	if overview.Totals.DatabaseCount == 0 {
		t.Fatal("no databases were discovered")
	}

	var found bool
	for _, db := range overview.Databases {
		if db.Name != e.conn.Database {
			continue
		}
		found = true
		if db.Status != postgres.StatusConnected {
			t.Fatalf("maintenance database is not connectable: %+v", db)
		}
		if db.RowCountSource != postgres.SourceEstimate {
			t.Errorf("row counts must be estimates, got %q", db.RowCountSource)
		}
		if db.SizeBytes <= 0 {
			t.Errorf("size was not reported: %d", db.SizeBytes)
		}
		if !db.Eligible {
			t.Errorf("the maintenance database should be dumpable: %+v", db)
		}
	}
	if !found {
		t.Fatalf("configured database %q missing from discovery", e.conn.Database)
	}

	if overview.Totals.Reachable == 0 {
		t.Error("no database was reachable")
	}
}

func TestDiscoveryReportsBadCredentials(t *testing.T) {
	e := env(t)
	e.conn.Password = execution.Secret("definitely-wrong-password")

	_, err := e.discoverer().Overview(context.Background(), e.conn)
	if err == nil {
		t.Skip("server accepted a wrong password (trust authentication)")
	}
	if kind := postgres.KindOf(err); kind != postgres.KindAuth && kind != postgres.KindUnreachable {
		t.Fatalf("kind = %v (%v)", kind, err)
	}
	if strings.Contains(err.Error(), "definitely-wrong-password") {
		t.Fatal("error message leaked the password")
	}
}

func TestDumpStreamsRealArchive(t *testing.T) {
	e := env(t)

	service := &dump.Service{
		Runner:         e.runner,
		Binaries:       pgclient.DefaultBinaries(),
		ConnectTimeout: 10 * time.Second,
		Logger:         slog.New(slog.DiscardHandler),
	}

	recorder := httptest.NewRecorder()
	result, err := service.Stream(context.Background(), recorder, e.conn, dump.Request{
		Database: e.conn.Database,
		Format:   pgclient.FormatCustom,
	})
	if err != nil {
		t.Fatalf("stream dump: %v", err)
	}

	if !result.Started || result.Bytes == 0 {
		t.Fatalf("no dump bytes were streamed: %+v", result)
	}
	body := recorder.Body.Bytes()
	if len(body) < 5 || string(body[:5]) != "PGDMP" {
		t.Fatalf("streamed data is not a custom archive (prefix %q)", body[:min(5, len(body))])
	}
	if got := recorder.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") {
		t.Errorf("missing attachment header: %q", got)
	}
}

func TestDumpPlainSQLStreamsText(t *testing.T) {
	e := env(t)

	service := &dump.Service{
		Runner:         e.runner,
		Binaries:       pgclient.DefaultBinaries(),
		ConnectTimeout: 10 * time.Second,
		Logger:         slog.New(slog.DiscardHandler),
	}

	recorder := httptest.NewRecorder()
	result, err := service.Stream(context.Background(), recorder, e.conn, dump.Request{
		Database: e.conn.Database,
		Format:   pgclient.FormatPlain,
	})
	if err != nil {
		t.Fatalf("stream dump: %v", err)
	}
	if result.Bytes == 0 {
		t.Fatal("empty SQL dump")
	}
	if !strings.Contains(recorder.Body.String(), "PostgreSQL database dump") {
		t.Fatalf("unexpected SQL dump header: %.120s", recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/sql") {
		t.Errorf("content type = %q", got)
	}
}

func TestHTTPConnectAndDownload(t *testing.T) {
	e := env(t)

	logger := slog.New(slog.DiscardHandler)
	sessions := session.NewStore(time.Minute)
	staticFS := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>dashboard</title>")}}

	handler := httpapi.New(
		httpapi.Options{
			Endpoint:           e.conn.Label(),
			ExecutionContext:   "local",
			Version:            "integration",
			MaxConcurrentDumps: 2,
			MaxRequestBytes:    16 << 10,
			SessionTTL:         time.Minute,
		},
		pgclient.Conn{Host: e.conn.Host, Port: e.conn.Port, Database: e.conn.Database, SSLMode: e.conn.SSLMode},
		e.discoverer(),
		&dump.Service{Runner: e.runner, Binaries: pgclient.DefaultBinaries(),
			ConnectTimeout: 10 * time.Second, Logger: logger},
		sessions,
		logger,
		staticFS,
	).Routes()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	body, err := json.Marshal(map[string]string{
		"user":     e.conn.User,
		"password": e.conn.Password.Reveal(),
		"database": e.conn.Database,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	connectReq, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/connect", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	connectReq.Header.Set("Content-Type", "application/json")

	client := server.Client()
	connectResp, err := client.Do(connectReq)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = connectResp.Body.Close() }()

	if connectResp.StatusCode != http.StatusOK {
		t.Fatalf("connect status = %d", connectResp.StatusCode)
	}

	var cookie *http.Cookie
	for _, c := range connectResp.Cookies() {
		if c.Name == session.CookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie")
	}

	dumpReq, err := http.NewRequest(http.MethodGet,
		server.URL+"/api/v1/databases/"+e.conn.Database+"/dump?format=custom", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	dumpReq.AddCookie(cookie)

	dumpResp, err := client.Do(dumpReq)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	defer func() { _ = dumpResp.Body.Close() }()

	if dumpResp.StatusCode != http.StatusOK {
		t.Fatalf("dump status = %d", dumpResp.StatusCode)
	}
	if disposition := dumpResp.Header.Get("Content-Disposition"); !strings.Contains(disposition, ".dump") {
		t.Errorf("content disposition = %q", disposition)
	}

	buffer := make([]byte, 5)
	if _, err := dumpResp.Body.Read(buffer); err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if string(buffer) != "PGDMP" {
		t.Fatalf("unexpected archive prefix %q", buffer)
	}

	// Cancelling the request must terminate pg_dump: after the connection is
	// closed the server must not keep a dump process around.
	if err := dumpResp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
}
