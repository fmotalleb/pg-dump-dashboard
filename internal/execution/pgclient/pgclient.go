// Package pgclient builds argument vectors for the PostgreSQL client tools.
//
// Everything the application knows about talking to PostgreSQL lives here:
// connection parameters are translated into libpq environment variables so that
// passwords never appear on a command line, and database names are validated
// before they are ever placed into an argument.
//
// The produced execution.Command values are transported to whichever execution
// context is configured (local, docker, later ssh). Nothing in this package
// spawns a process.
package pgclient

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
)

// Binaries names the client executables used inside the execution context.
// Bare names are resolved through the execution context's PATH.
type Binaries struct {
	Psql   string
	PgDump string
}

// DefaultBinaries returns the conventional executable names.
func DefaultBinaries() Binaries {
	return Binaries{Psql: "psql", PgDump: "pg_dump"}
}

// Validate checks that both binary names are set.
func (b Binaries) Validate() error {
	if b.Psql == "" {
		return fmt.Errorf("psql binary name must not be empty")
	}
	if b.PgDump == "" {
		return fmt.Errorf("pg_dump binary name must not be empty")
	}
	return nil
}

// Conn holds the effective connection parameters for one request.
type Conn struct {
	Host     string
	Port     uint16
	User     string
	Database string
	SSLMode  string
	// Password is transmitted through the process environment only.
	Password execution.Secret
	// ConnectTimeout bounds the time libpq spends establishing a connection.
	ConnectTimeout time.Duration
}

// Env renders the connection parameters as libpq environment variables.
// The returned slice contains the password; it must never be logged.
func (c Conn) Env() []string {
	env := []string{
		"PGAPPNAME=pg-dump-dashboard",
		"PGCONNECT_TIMEOUT=" + strconv.Itoa(seconds(c.ConnectTimeout)),
	}
	if c.Host != "" {
		env = append(env, "PGHOST="+c.Host)
	}
	if c.Port != 0 {
		env = append(env, "PGPORT="+strconv.Itoa(int(c.Port)))
	}
	if c.User != "" {
		env = append(env, "PGUSER="+c.User)
	}
	if c.Database != "" {
		env = append(env, "PGDATABASE="+c.Database)
	}
	if c.SSLMode != "" {
		env = append(env, "PGSSLMODE="+c.SSLMode)
	}
	if !c.Password.IsZero() {
		env = append(env, "PGPASSWORD="+c.Password.Reveal())
	}
	return env
}

// Label returns a human readable, log-safe description of the target.
func (c Conn) Label() string {
	host := c.Host
	if host == "" {
		host = "localhost"
	}
	return host + ":" + strconv.Itoa(int(c.Port))
}

func seconds(d time.Duration) int {
	if d <= 0 {
		return 10
	}
	secs := int(d.Round(time.Second) / time.Second)
	if secs < 1 {
		return 1
	}
	return secs
}

// ScalarQuery runs a statement in single-transaction mode and returns the raw
// psql text output. The SQL itself is delivered on standard input, never as an
// argument.
func ScalarQuery(bin Binaries, conn Conn, sql string) execution.Command {
	return execution.Command{
		Path: bin.Psql,
		Args: []string{
			"--no-psqlrc",
			"--quiet",
			"--no-password",
			"--no-align",
			"--tuples-only",
			"--field-separator", "\x1f",
			"--set", "ON_ERROR_STOP=1",
			"--set", "QUIET=1",
			"--file", "-",
		},
		Env:   conn.Env(),
		Stdin: sql + "\n",
	}
}

// DumpFormat selects the pg_dump output format.
type DumpFormat string

// Supported pg_dump formats.
const (
	// FormatPlain emits SQL text and can be piped through psql.
	FormatPlain DumpFormat = "plain"
	// FormatCustom emits the compressed custom archive understood by pg_restore.
	FormatCustom DumpFormat = "custom"
)

// Valid reports whether the format is one of the supported values.
func (f DumpFormat) Valid() bool {
	return f == FormatPlain || f == FormatCustom
}

// DumpOptions tunes a pg_dump invocation.
type DumpOptions struct {
	Format DumpFormat
	// Schema restricts the dump to a single schema. Optional.
	Schema string
}

// DumpCommand builds the pg_dump invocation for one database. The dump is
// written to standard output so it can be streamed to the client.
func DumpCommand(bin Binaries, conn Conn, opts DumpOptions) (execution.Command, error) {
	if err := ValidDatabaseName(conn.Database); err != nil {
		return execution.Command{}, err
	}
	format := opts.Format
	if format == "" {
		format = FormatCustom
	}
	if !format.Valid() {
		return execution.Command{}, fmt.Errorf("unsupported dump format %q", format)
	}

	args := []string{
		"--no-password",
		"--format=" + string(format),
		"--verbose",
	}
	if format == FormatCustom {
		// Custom archives are compressed; plain SQL text must stay readable so
		// it can be replayed with psql.
		args = append(args, "--compress=6")
	}
	if opts.Schema != "" {
		if err := ValidDatabaseName(opts.Schema); err != nil {
			return execution.Command{}, fmt.Errorf("invalid schema filter: %w", err)
		}
		args = append(args, "--schema="+opts.Schema)
	}
	// Database names are validated above; "--" guards against a value being
	// mistaken for a flag even if validation ever loosens.
	args = append(args, "--", conn.Database)

	return execution.Command{
		Path: bin.PgDump,
		Args: args,
		Env:  conn.Env(),
	}, nil
}

// maxIdentifierLen mirrors PostgreSQL's NAMEDATALEN-1 limit.
const maxIdentifierLen = 63

// ValidDatabaseName reports whether name is a safe, unambiguous database name.
//
// The name is passed to psql and pg_dump as a single argument. The accepted
// alphabet is deliberately narrow: anything that could be interpreted as a
// flag, a path separator, a quote or a shell metacharacter is rejected.
func ValidDatabaseName(name string) error {
	if name == "" {
		return fmt.Errorf("database name must not be empty")
	}
	if len(name) > maxIdentifierLen {
		return fmt.Errorf("database name must be at most %d bytes", maxIdentifierLen)
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("database name must not start with '-'")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_', r == '-', r == '.', r == '$':
		default:
			return fmt.Errorf("database name contains an unsupported character %q", r)
		}
	}
	return nil
}
