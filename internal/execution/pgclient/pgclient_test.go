package pgclient_test

import (
	"strings"
	"testing"
	"time"

	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/pgclient"
)

func testConn() pgclient.Conn {
	return pgclient.Conn{
		Host:           "db.internal",
		Port:           5433,
		User:           "reporter",
		Database:       "appdb",
		SSLMode:        "require",
		Password:       execution.Secret("s3cr3t-value"),
		ConnectTimeout: 7 * time.Second,
	}
}

func TestConnEnvCarriesPasswordAndParameters(t *testing.T) {
	t.Parallel()

	env := strings.Join(testConn().Env(), " ")
	for _, want := range []string{
		"PGHOST=db.internal",
		"PGPORT=5433",
		"PGUSER=reporter",
		"PGDATABASE=appdb",
		"PGSSLMODE=require",
		"PGCONNECT_TIMEOUT=7",
		"PGPASSWORD=s3cr3t-value",
		"PGAPPNAME=pg-dump-dashboard",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("environment is missing %q: %s", want, env)
		}
	}
}

func TestScalarQueryKeepsSecretsAndSQLOutOfArgv(t *testing.T) {
	t.Parallel()

	conn := testConn()
	cmd := pgclient.ScalarQuery(pgclient.DefaultBinaries(), conn, "SELECT 1;")

	if cmd.Path != "psql" {
		t.Errorf("path = %q", cmd.Path)
	}
	joined := cmd.Describe()
	if strings.Contains(joined, "s3cr3t-value") {
		t.Fatalf("password leaked into argv: %s", joined)
	}
	if strings.Contains(joined, "SELECT 1") {
		t.Fatalf("SQL leaked into argv: %s", joined)
	}
	if cmd.Stdin != "SELECT 1;\n" {
		t.Errorf("SQL should be delivered on stdin, got %q", cmd.Stdin)
	}
	for _, want := range []string{"--no-psqlrc", "--no-password", "--file -", "--set ON_ERROR_STOP=1", "--tuples-only"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q: %s", want, joined)
		}
	}
	if len(cmd.Env) == 0 || !strings.Contains(strings.Join(cmd.Env, " "), "PGPASSWORD=") {
		t.Fatalf("password must be provided through the environment")
	}
}

func TestDumpCommandConstruction(t *testing.T) {
	t.Parallel()

	conn := testConn()

	cmd, err := pgclient.DumpCommand(pgclient.DefaultBinaries(), conn, pgclient.DumpOptions{Format: pgclient.FormatCustom})
	if err != nil {
		t.Fatalf("build dump command: %v", err)
	}
	if cmd.Path != "pg_dump" {
		t.Errorf("path = %q", cmd.Path)
	}
	joined := cmd.Describe()
	for _, want := range []string{"--no-password", "--format=custom", "--compress=6", "--verbose", "-- appdb"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "s3cr3t-value") {
		t.Fatalf("password leaked into argv: %s", joined)
	}

	plain, err := pgclient.DumpCommand(pgclient.DefaultBinaries(), conn, pgclient.DumpOptions{Format: pgclient.FormatPlain})
	if err != nil {
		t.Fatalf("build plain dump command: %v", err)
	}
	if strings.Contains(plain.Describe(), "--compress") {
		t.Errorf("plain SQL dumps must not be compressed: %s", plain.Describe())
	}

	withSchema, err := pgclient.DumpCommand(pgclient.DefaultBinaries(), conn,
		pgclient.DumpOptions{Format: pgclient.FormatCustom, Schema: "sales"})
	if err != nil {
		t.Fatalf("build schema dump command: %v", err)
	}
	if !strings.Contains(withSchema.Describe(), "--schema=sales") {
		t.Errorf("schema filter missing: %s", withSchema.Describe())
	}
}

func TestDumpCommandRejectsUnsafeInput(t *testing.T) {
	t.Parallel()

	conn := testConn()
	conn.Database = "appdb; DROP DATABASE appdb"
	if _, err := pgclient.DumpCommand(pgclient.DefaultBinaries(), conn, pgclient.DumpOptions{}); err == nil {
		t.Fatal("expected an error for an unsafe database name")
	}

	conn = testConn()
	if _, err := pgclient.DumpCommand(pgclient.DefaultBinaries(), conn,
		pgclient.DumpOptions{Format: "tar"}); err == nil {
		t.Fatal("expected an error for an unsupported format")
	}

	if _, err := pgclient.DumpCommand(pgclient.DefaultBinaries(), conn,
		pgclient.DumpOptions{Schema: "../etc"}); err == nil {
		t.Fatal("expected an error for an unsafe schema name")
	}
}

func TestValidDatabaseName(t *testing.T) {
	t.Parallel()

	valid := []string{"postgres", "app-db", "app.db", "app_db", "AppDB2", "db$1", strings.Repeat("a", 63)}
	for _, name := range valid {
		if err := pgclient.ValidDatabaseName(name); err != nil {
			t.Errorf("%q should be valid: %v", name, err)
		}
	}

	invalid := []string{
		"",
		strings.Repeat("a", 64),
		"-leading-dash",
		"has space",
		"semi;colon",
		"quote'name",
		"back\\slash",
		"path/traversal",
		"new\nline",
		"$(id)",
		"`whoami`",
		"ünïcode",
	}
	for _, name := range invalid {
		if err := pgclient.ValidDatabaseName(name); err == nil {
			t.Errorf("%q should be rejected", name)
		}
	}
}

func TestBinariesValidate(t *testing.T) {
	t.Parallel()

	if err := pgclient.DefaultBinaries().Validate(); err != nil {
		t.Fatalf("default binaries must validate: %v", err)
	}
	if err := (pgclient.Binaries{Psql: "psql"}).Validate(); err == nil {
		t.Fatal("expected an error when pg_dump is missing")
	}
}
