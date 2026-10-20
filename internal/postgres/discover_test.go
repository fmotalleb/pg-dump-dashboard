package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/fake"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/pgclient"
	"github.com/fmotalleb/pg-dump-dashboard/internal/postgres"
)

const sep = "\x1f"

// serverOutput is a realistic psql rendering of the discovery query: two
// databases plus one without CONNECT privilege.
func serverOutput() string {
	rows := []string{
		strings.Join([]string{"srv", "17.2 (Debian 17.2-1)", "reporter", "postgres"}, sep),
		strings.Join([]string{"db", "analytics", "reporter", "8192000", "UTF8", "en_US.utf8", "en_US.utf8", "t", "t", "2"}, sep),
		strings.Join([]string{"db", "appdb", "reporter", "4096000", "UTF8", "en_US.utf8", "en_US.utf8", "t", "t", "1"}, sep),
		strings.Join([]string{"db", "secret", "postgres", "-1", "UTF8", "C", "C", "t", "f", "0"}, sep),
		strings.Join([]string{"db", "noconn", "postgres", "-1", "UTF8", "C", "C", "f", "t", "0"}, sep),
	}
	return strings.Join(rows, "\n") + "\n"
}

// databaseOutput is a realistic psql rendering of the per-database query.
func databaseOutput() string {
	rows := []string{
		strings.Join([]string{"schema", "3"}, sep),
		strings.Join([]string{"obj", "r", "12"}, sep),
		strings.Join([]string{"obj", "v", "2"}, sep),
		strings.Join([]string{"obj", "m", "1"}, sep),
		strings.Join([]string{"obj", "S", "4"}, sep),
		strings.Join([]string{"obj", "i", "9"}, sep),
		strings.Join([]string{"obj", "p", "1"}, sep),
		strings.Join([]string{"fn", "7"}, sep),
		strings.Join([]string{"rows", "1500", "13"}, sep),
		strings.Join([]string{"table", "sales", "orders", "1000", "262144"}, sep),
		strings.Join([]string{"table", "public", "customers", "500", "163840"}, sep),
	}
	return strings.Join(rows, "\n") + "\n"
}

func newDiscoverer(runner execution.Runner) *postgres.Discoverer {
	return &postgres.Discoverer{
		Runner:         runner,
		Binaries:       pgclient.DefaultBinaries(),
		ConnectTimeout: 5 * time.Second,
		QueryTimeout:   5 * time.Second,
		Timeout:        10 * time.Second,
		Concurrency:    2,
	}
}

func testConn() pgclient.Conn {
	return pgclient.Conn{
		Host:     "db",
		Port:     5432,
		User:     "reporter",
		Password: execution.Secret("top-secret"),
		Database: "postgres",
	}
}

func TestOverviewParsesCatalogData(t *testing.T) {
	t.Parallel()

	runner := fake.New(
		fake.Response{Stdout: serverOutput()},
		fake.Response{Stdout: databaseOutput()},
		fake.Response{Stdout: databaseOutput()},
	)
	overview, err := newDiscoverer(runner).Overview(context.Background(), testConn())
	if err != nil {
		t.Fatalf("overview: %v", err)
	}

	if overview.ServerVersion != "17.2 (Debian 17.2-1)" {
		t.Errorf("server version = %q", overview.ServerVersion)
	}
	if overview.ConnectedAs != "reporter" || overview.ConnectedTo != "postgres" {
		t.Errorf("identity = %q@%q", overview.ConnectedAs, overview.ConnectedTo)
	}
	if overview.ExecutionContext != "fake" {
		t.Errorf("execution context = %q", overview.ExecutionContext)
	}
	if overview.ObjectCountSource != postgres.SourceCatalog {
		t.Errorf("object counts must be exact (catalog), got %q", overview.ObjectCountSource)
	}
	if overview.RowCountSource != postgres.SourceEstimate {
		t.Errorf("row counts must be marked as estimates, got %q", overview.RowCountSource)
	}

	if len(overview.Databases) != 4 {
		t.Fatalf("database count = %d", len(overview.Databases))
	}

	byName := map[string]postgres.Database{}
	for _, db := range overview.Databases {
		byName[db.Name] = db
	}

	appdb := byName["appdb"]
	if appdb.Status != postgres.StatusConnected {
		t.Fatalf("appdb status = %q (%s)", appdb.Status, appdb.Error)
	}
	if appdb.RowCountSource != postgres.SourceEstimate || appdb.RowEstimate != 1500 {
		t.Errorf("appdb rows = %d (%q)", appdb.RowEstimate, appdb.RowCountSource)
	}
	if appdb.ObjectCounts.Schemas != 3 || appdb.ObjectCounts.Tables != 13 || appdb.ObjectCounts.Views != 3 {
		t.Errorf("unexpected object counts: %+v", appdb.ObjectCounts)
	}
	if appdb.ObjectCounts.MaterializedViews != 1 || appdb.ObjectCounts.Indexes != 9 {
		t.Errorf("unexpected object counts: %+v", appdb.ObjectCounts)
	}
	if len(appdb.LargestTables) != 2 || appdb.LargestTables[0].Name != "orders" {
		t.Errorf("largest tables = %+v", appdb.LargestTables)
	}
	if !appdb.LargestTables[0].RowsAreEstimate {
		t.Error("relation row counts must be flagged as estimates")
	}
	if !appdb.Eligible {
		t.Error("appdb should be eligible for dumping")
	}

	if db := byName["secret"]; db.Status != postgres.StatusRestricted || db.Eligible {
		t.Errorf("a database without CONNECT must be restricted: %+v", db)
	} else if !strings.Contains(db.Error, "CONNECT") {
		t.Errorf("restricted reason = %q", db.Error)
	}

	if db := byName["noconn"]; db.Status != postgres.StatusRestricted || db.Eligible {
		t.Errorf("datallowconn=false must be restricted: %+v", db)
	}

	// Only appdb and analytics are inspected: 2 schemas + 3 * 2, etc.
	if overview.Totals.DatabaseCount != 4 || overview.Totals.Reachable != 2 || overview.Totals.Unreachable != 2 {
		t.Errorf("unexpected totals: %+v", overview.Totals)
	}
	if overview.Totals.Schemas != 6 {
		t.Errorf("total schemas = %d, want 6", overview.Totals.Schemas)
	}
	if overview.Totals.SizeBytes != 8192000+4096000 {
		t.Errorf("total size = %d", overview.Totals.SizeBytes)
	}
	if overview.Totals.RowEstimate != 3000 {
		t.Errorf("total row estimate = %d", overview.Totals.RowEstimate)
	}
}

func TestOverviewPasswordStaysInEnvironment(t *testing.T) {
	t.Parallel()

	runner := fake.New(
		fake.Response{Stdout: serverOutput()},
		fake.Response{Stdout: databaseOutput()},
		fake.Response{Stdout: databaseOutput()},
	)
	if _, err := newDiscoverer(runner).Overview(context.Background(), testConn()); err != nil {
		t.Fatalf("overview: %v", err)
	}

	for _, cmd := range runner.Commands() {
		if strings.Contains(cmd.Describe(), "top-secret") {
			t.Fatalf("password appeared in argv: %s", cmd.Describe())
		}
		if !strings.Contains(strings.Join(cmd.Env, " "), "PGPASSWORD=top-secret") {
			t.Fatalf("password was not passed through the environment: %v", cmd.Env)
		}
	}
}

func TestOverviewClassifiesAuthenticationFailure(t *testing.T) {
	t.Parallel()

	runner := fake.New()
	runner.RunErr = &execution.ExitError{
		Command: "psql",
		Code:    2,
		Stderr:  `psql: error: connection to server at "db", port 5432 failed: FATAL:  password authentication failed for user "reporter"`,
	}

	_, err := newDiscoverer(runner).Overview(context.Background(), testConn())
	if err == nil {
		t.Fatal("expected an error")
	}
	if kind := postgres.KindOf(err); kind != postgres.KindAuth {
		t.Fatalf("kind = %v, want KindAuth", kind)
	}
	if strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("error leaked the password: %v", err)
	}
	if strings.Contains(err.Error(), "--field-separator") {
		t.Fatalf("error leaked the full argument vector: %v", err)
	}
}

func TestOverviewClassifiesUnreachableServer(t *testing.T) {
	t.Parallel()

	runner := fake.New()
	runner.RunErr = &execution.ExitError{
		Command: "psql",
		Code:    2,
		Stderr:  `psql: error: connection to server at "db" (10.0.0.1), port 5432 failed: Connection refused`,
	}

	_, err := newDiscoverer(runner).Overview(context.Background(), testConn())
	if kind := postgres.KindOf(err); kind != postgres.KindUnreachable {
		t.Fatalf("kind = %v, want KindUnreachable (%v)", kind, err)
	}
}

func TestOverviewReportsMissingClientBinaries(t *testing.T) {
	t.Parallel()

	runner := fake.New()
	runner.RunErr = errors.Join(execution.ErrBinaryNotFound, errors.New("psql"))

	_, err := newDiscoverer(runner).Overview(context.Background(), testConn())
	if kind := postgres.KindOf(err); kind != postgres.KindClientMissing {
		t.Fatalf("kind = %v, want KindClientMissing (%v)", kind, err)
	}
}

func TestOverviewRejectsMalformedOutput(t *testing.T) {
	t.Parallel()

	runner := fake.New(fake.Response{Stdout: "garbage\n"})
	_, err := newDiscoverer(runner).Overview(context.Background(), testConn())
	if err == nil {
		t.Fatal("expected an error for malformed psql output")
	}
}

func TestPerDatabaseFailureDoesNotAbortDiscovery(t *testing.T) {
	t.Parallel()

	runner := fake.New(
		fake.Response{Stdout: serverOutput()},
		fake.Response{Err: &execution.ExitError{Command: "psql", Code: 1,
			Stderr: "psql: error: connection to server failed: FATAL: permission denied for database"}},
		fake.Response{Stdout: databaseOutput()},
	)

	overview, err := newDiscoverer(runner).Overview(context.Background(), testConn())
	if err != nil {
		t.Fatalf("a single inaccessible database must not fail discovery: %v", err)
	}
	if overview.Totals.Reachable != 1 || overview.Totals.Unreachable != 3 {
		t.Fatalf("unexpected totals: %+v", overview.Totals)
	}
}

func TestTimeoutIsClassified(t *testing.T) {
	t.Parallel()

	runner := fake.New()
	runner.RunErr = context.DeadlineExceeded

	discoverer := newDiscoverer(runner)
	discoverer.Timeout = 5 * time.Millisecond

	_, err := discoverer.Overview(context.Background(), testConn())
	if kind := postgres.KindOf(err); kind != postgres.KindTimeout {
		t.Fatalf("kind = %v, want KindTimeout (%v)", kind, err)
	}
}

func TestMaxDatabasesLimit(t *testing.T) {
	t.Parallel()

	runner := fake.New(
		fake.Response{Stdout: serverOutput()},
		fake.Response{Stdout: databaseOutput()},
	)
	discoverer := newDiscoverer(runner)
	discoverer.MaxDatabases = 1

	overview, err := discoverer.Overview(context.Background(), testConn())
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.Totals.Reachable != 1 {
		t.Fatalf("expected exactly one inspected database, got %+v", overview.Totals)
	}
	if calls := len(runner.Commands()); calls != 2 {
		t.Fatalf("expected 1 server query + 1 database query, got %d calls", calls)
	}
}

func TestClassifyFallsBackToInternal(t *testing.T) {
	t.Parallel()

	err := postgres.Classify("discover", errors.New("something odd happened"))
	if postgres.KindOf(err) != postgres.KindInternal {
		t.Fatalf("kind = %v", postgres.KindOf(err))
	}
	if !strings.Contains(err.Error(), "something odd happened") {
		t.Fatalf("message = %q", err.Error())
	}
}
