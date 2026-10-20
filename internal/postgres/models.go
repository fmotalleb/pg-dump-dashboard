// Package postgres discovers databases and their metadata by running the
// PostgreSQL client tools through an execution backend.
package postgres

import "time"

// Source describes where a number came from. It is rendered in the UI so
// operators never mistake an estimate for a fact.
type Source string

// Possible values for Source.
const (
	// SourceCatalog marks values counted directly from system catalogs.
	SourceCatalog Source = "catalog"
	// SourceEstimate marks values derived from statistics (pg_class.reltuples).
	SourceEstimate Source = "estimate"
	// SourceUnavailable marks values that could not be determined.
	SourceUnavailable Source = "unavailable"
)

// Status summarises how a database behaved during discovery.
type Status string

// Possible values for Status.
const (
	// StatusConnected means the database was queried successfully.
	StatusConnected Status = "connected"
	// StatusRestricted means access was denied (missing CONNECT privilege).
	StatusRestricted Status = "restricted"
	// StatusError means the database could not be inspected for another reason.
	StatusError Status = "error"
)

// Overview is the whole discovery result for one connection.
type Overview struct {
	// ServerVersion is the value of server_version reported by PostgreSQL.
	ServerVersion string `json:"serverVersion"`
	// ServerVersionFull is the human readable version() string.
	ServerVersionFull string `json:"serverVersionFull"`
	// Endpoint is the log-safe "host:port" that was queried.
	Endpoint string `json:"endpoint"`
	// ExecutionContext names the backend used ("local", "docker", ...).
	ExecutionContext string `json:"executionContext"`
	// ConnectedAs is the role the session is running as.
	ConnectedAs string `json:"connectedAs"`
	// ConnectedTo is the maintenance database used for discovery.
	ConnectedTo string `json:"connectedTo"`
	// CollectedAt is the moment discovery finished.
	CollectedAt time.Time `json:"collectedAt"`
	// ObjectCountSource describes how object counts were obtained.
	ObjectCountSource Source `json:"objectCountSource"`
	// RowCountSource describes how row counts were obtained.
	RowCountSource Source `json:"rowCountSource"`
	// Databases holds one entry per non-template database.
	Databases []Database `json:"databases"`
	// Totals aggregates what is known across databases.
	Totals Totals `json:"totals"`
}

// Totals aggregates catalog statistics across every inspected database.
type Totals struct {
	DatabaseCount int `json:"databaseCount"`
	Reachable     int `json:"reachable"`
	Unreachable   int `json:"unreachable"`
	Schemas       int `json:"schemas"`
	Tables        int `json:"tables"`
	Views         int `json:"views"`
	Sequences     int `json:"sequences"`
	Functions     int `json:"functions"`
	Indexes       int `json:"indexes"`
	// RowEstimate sums pg_class.reltuples over every counted table.
	RowEstimate int64 `json:"rowEstimate"`
	// SizeBytes sums pg_database_size for reachable databases.
	SizeBytes int64 `json:"sizeBytes"`
	// Connections is the number of client backends reported by pg_stat_database.
	Connections int `json:"connections"`
}

// Database is the per-database metadata shown in the dashboard.
type Database struct {
	Name   string `json:"name"`
	Owner  string `json:"owner"`
	Status Status `json:"status"`
	// Error is a log-safe explanation when Status is not connected.
	Error string `json:"error,omitempty"`
	// SizeBytes is the on-disk size, or -1 when it is not readable.
	SizeBytes int64  `json:"sizeBytes"`
	Encoding  string `json:"encoding"`
	Collate   string `json:"collate"`
	CType     string `json:"ctype"`
	// AllowConnections mirrors pg_database.datallowconn.
	AllowConnections bool `json:"allowConnections"`
	// ConnectPrivilege mirrors has_database_privilege(name, 'CONNECT').
	ConnectPrivilege bool `json:"connectPrivilege"`
	// Connections is pg_stat_database.numbackends.
	Connections int `json:"connections"`

	ObjectCounts ObjectCounts `json:"objectCounts"`
	// RowEstimate is the sum of pg_class.reltuples over regular tables.
	RowEstimate int64 `json:"rowEstimate"`
	// RowCountSource is estimate, catalog or unavailable.
	RowCountSource Source `json:"rowCountSource"`
	// Eligible reports whether a dump can be attempted (computed during
	// discovery so the UI does not have to re-derive it).
	Eligible bool `json:"eligible"`
	// LargestTables holds up to five tables ordered by total size.
	LargestTables []TableStat `json:"largestTables,omitempty"`
}

// ObjectCounts holds per-database catalog object counts (exact).
type ObjectCounts struct {
	Schemas           int `json:"schemas"`
	Tables            int `json:"tables"`
	Views             int `json:"views"`
	MaterializedViews int `json:"materializedViews"`
	Sequences         int `json:"sequences"`
	Indexes           int `json:"indexes"`
	Functions         int `json:"functions"`
}

// TableStat describes a single relation.
type TableStat struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	// Rows is pg_class.reltuples; negative values mean "never analysed".
	Rows int64 `json:"rows"`
	// RowsAreEstimate is always true: reltuples is a planner statistic.
	RowsAreEstimate bool  `json:"rowsAreEstimate"`
	SizeBytes       int64 `json:"sizeBytes"`
}

// CanDump reports whether a dump can be attempted for this database.
func (d Database) CanDump() bool {
	return d.Status == StatusConnected && d.AllowConnections && d.ConnectPrivilege
}

// DumpFilename builds the attachment name for a database dump.
func DumpFilename(database, format string) string {
	return database + "-" + time.Now().UTC().Format("20060102T150405Z") + dumpExtension(format)
}

func dumpExtension(format string) string {
	if format == "plain" {
		return ".sql"
	}
	return ".dump"
}
