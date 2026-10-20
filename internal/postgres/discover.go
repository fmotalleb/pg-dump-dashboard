package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/pgclient"
)

// Defaults for discovery tuning.
const (
	DefaultQueryTimeout = 15 * time.Second
	DefaultConcurrency  = 4
)

// Discoverer reads database metadata through an execution backend.
//
// It is safe for concurrent use; it holds no mutable state.
type Discoverer struct {
	Runner   execution.Runner
	Binaries pgclient.Binaries
	// ConnectTimeout bounds libpq's connection attempt (PGCONNECT_TIMEOUT).
	ConnectTimeout time.Duration
	// QueryTimeout bounds a single psql invocation.
	QueryTimeout time.Duration
	// Concurrency bounds how many databases are inspected in parallel.
	Concurrency int
	// Timeout bounds the whole discovery.
	Timeout time.Duration
	// MaxDatabases caps how many databases are inspected. Zero means no cap.
	MaxDatabases int
}

func (d *Discoverer) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return 30 * time.Second
}

func (d *Discoverer) queryTimeout() time.Duration {
	if d.QueryTimeout > 0 {
		return d.QueryTimeout
	}
	return DefaultQueryTimeout
}

func (d *Discoverer) concurrency() int {
	if d.Concurrency > 0 {
		return d.Concurrency
	}
	return DefaultConcurrency
}

// Overview inspects the server reachable with conn.Database and returns the
// database list with per-database metadata.
//
// Failures that prevent the listing entirely are returned as *Error; failures
// that concern a single database are recorded on that Database and do not fail
// the whole request.
func (d *Discoverer) Overview(ctx context.Context, conn pgclient.Conn) (*Overview, error) {
	if d.Runner == nil {
		return nil, &Error{Kind: KindInternal, Message: "no execution backend configured"}
	}

	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()

	conn.ConnectTimeout = d.ConnectTimeout

	serverCmd := pgclient.ScalarQuery(d.Binaries, conn, serverQuery)
	result, err := d.Runner.Run(ctx, serverCmd)
	if err != nil {
		return nil, Classify("connect to "+conn.Label(), err)
	}
	if len(result.Stdout) == 0 {
		return nil, &Error{Kind: KindInternal, Message: "connect to " + conn.Label() + ": empty response from psql"}
	}

	version, currentUser, currentDB, dbs, err := parseServerResult(string(result.Stdout))
	if err != nil {
		return nil, &Error{Kind: KindInternal, Message: "parse server metadata: " + err.Error(), Err: err}
	}

	overview := &Overview{
		ServerVersion:     version,
		ServerVersionFull: "PostgreSQL " + version,
		Endpoint:          conn.Label(),
		ExecutionContext:  d.Runner.Name(),
		ConnectedAs:       currentUser,
		ConnectedTo:       currentDB,
		CollectedAt:       time.Now().UTC(),
		ObjectCountSource: SourceCatalog,
		RowCountSource:    SourceEstimate,
		Databases:         make([]Database, len(dbs)),
	}

	for i, entry := range dbs {
		overview.Databases[i] = Database{
			Name:             entry.name,
			Owner:            entry.owner,
			SizeBytes:        entry.sizeBytes,
			Encoding:         entry.encoding,
			Collate:          entry.collate,
			CType:            entry.ctype,
			AllowConnections: entry.allowConnections,
			ConnectPrivilege: entry.connectPrivilege,
			Connections:      entry.connections,
			Status:           StatusConnected,
			RowCountSource:   SourceUnavailable,
		}
	}

	// Databases that cannot be queried are marked without a round trip.
	var inspectable []int
	for i := range overview.Databases {
		db := &overview.Databases[i]
		switch {
		case !db.AllowConnections:
			db.Status = StatusRestricted
			db.Error = "connections are disabled for this database (datallowconn = false)"
		case !db.ConnectPrivilege:
			db.Status = StatusRestricted
			db.Error = "current role has no CONNECT privilege on this database"
		case d.MaxDatabases > 0 && len(inspectable) >= d.MaxDatabases:
			db.Status = StatusError
			db.Error = fmt.Sprintf("skipped: inspection limited to %d databases", d.MaxDatabases)
		default:
			inspectable = append(inspectable, i)
		}
	}

	d.inspectDatabases(ctx, conn, overview, inspectable)

	for i := range overview.Databases {
		overview.Databases[i].Eligible = overview.Databases[i].CanDump()
	}

	overview.Totals = aggregate(overview.Databases)
	return overview, nil
}

// inspectDatabases fills in catalog statistics for the given indices using a
// bounded worker pool.
func (d *Discoverer) inspectDatabases(ctx context.Context, conn pgclient.Conn, overview *Overview, indices []int) {
	if len(indices) == 0 {
		return
	}

	sem := make(chan struct{}, d.concurrency())
	var wg sync.WaitGroup

	for _, idx := range indices {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				markUnreachable(&overview.Databases[idx], Classify("inspect database", ctx.Err()))
				return
			}

			db := &overview.Databases[idx]
			dbConn := conn
			dbConn.Database = db.Name

			queryCtx, cancel := context.WithTimeout(ctx, d.queryTimeout())
			defer cancel()

			cmd := pgclient.ScalarQuery(d.Binaries, dbConn, databaseQuery)
			result, err := d.Runner.Run(queryCtx, cmd)
			if err != nil {
				markUnreachable(db, Classify("inspect "+db.Name, err))
				return
			}

			stats, err := parseDatabaseResult(string(result.Stdout))
			if err != nil {
				db.Status = StatusError
				db.Error = "parse metadata: " + err.Error()
				return
			}

			db.Status = StatusConnected
			db.ObjectCounts = stats.counts
			db.LargestTables = stats.tables
			if stats.hasRows {
				db.RowEstimate = stats.rows
				db.RowCountSource = SourceEstimate
			}
		}(idx)
	}

	wg.Wait()
}

func markUnreachable(db *Database, err error) {
	db.Status = StatusError
	db.Error = Reason(err)
	classify := KindOf(err)
	if classify == KindRestricted {
		db.Status = StatusRestricted
	}
	if errors.Is(err, context.DeadlineExceeded) {
		db.Status = StatusError
		db.Error = "timed out while inspecting this database"
	}
}

func aggregate(dbs []Database) Totals {
	totals := Totals{DatabaseCount: len(dbs)}
	for _, db := range dbs {
		if db.Status == StatusConnected {
			totals.Reachable++
			totals.Schemas += db.ObjectCounts.Schemas
			totals.Tables += db.ObjectCounts.Tables
			totals.Views += db.ObjectCounts.Views
			totals.Sequences += db.ObjectCounts.Sequences
			totals.Functions += db.ObjectCounts.Functions
			totals.Indexes += db.ObjectCounts.Indexes
			totals.RowEstimate += db.RowEstimate
		} else {
			totals.Unreachable++
		}
		if db.SizeBytes > 0 {
			totals.SizeBytes += db.SizeBytes
		}
		totals.Connections += db.Connections
	}
	return totals
}

// SortDatabases orders databases by name for stable output.
func SortDatabases(dbs []Database) {
	sort.Slice(dbs, func(i, j int) bool { return strings.ToLower(dbs[i].Name) < strings.ToLower(dbs[j].Name) })
}
