// Package config defines the command line interface and the validated runtime
// configuration of pg-dump-dashboard.
//
// PostgreSQL credentials are deliberately absent: they are supplied by the
// operator through the web UI for each session and are never stored.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/pgclient"
	"github.com/fmotalleb/pg-dump-dashboard/internal/version"
)

// Execution backend names.
const (
	ExecutionLocal  = "local"
	ExecutionDocker = "docker"
)

// validSSLModes lists the libpq sslmode values.
var validSSLModes = map[string]bool{
	"disable":     true,
	"allow":       true,
	"prefer":      true,
	"require":     true,
	"verify-ca":   true,
	"verify-full": true,
}

// validLogLevels lists accepted log levels.
var validLogLevels = map[string]bool{
	"debug": true,
	"info":  true,
	"warn":  true,
	"error": true,
}

// validLogFormats lists accepted log formats.
var validLogFormats = map[string]bool{
	"text": true,
	"json": true,
}

// Config is the validated application configuration.
type Config struct {
	// Listen is the HTTP listen address ("host:port").
	Listen string

	Postgres PostgresConfig
	// Execution selects the backend used to run psql and pg_dump.
	Execution ExecutionConfig

	// ConnectTimeout bounds a libpq connection attempt.
	ConnectTimeout time.Duration
	// QueryTimeout bounds a single metadata query.
	QueryTimeout time.Duration
	// MetadataTimeout bounds a whole metadata refresh.
	MetadataTimeout time.Duration
	// MetadataConcurrency bounds parallel per-database queries.
	MetadataConcurrency int
	// MetadataMaxDatabases caps how many databases are inspected.
	MetadataMaxDatabases int
	// DumpTimeout bounds a single download. Zero means unlimited.
	DumpTimeout time.Duration
	// MaxConcurrentDumps bounds simultaneous dump streams.
	MaxConcurrentDumps int

	// ShutdownTimeout bounds graceful shutdown.
	ShutdownTimeout time.Duration
	// ReadHeaderTimeout bounds header parsing.
	ReadHeaderTimeout time.Duration
	// MaxRequestBodyBytes bounds JSON request bodies.
	MaxRequestBodyBytes int64

	// CookieSecure marks the session cookie as Secure. Enable it when the UI is
	// served over TLS.
	CookieSecure bool

	// LogLevel is one of debug, info, warn, error.
	LogLevel string
	// LogFormat is either text or json.
	LogFormat string
}

// PostgresConfig describes the PostgreSQL server the UI connects to.
type PostgresConfig struct {
	Host string
	Port uint16
	// Database is the maintenance database used for discovery.
	Database string
	SSLMode  string
}

// Endpoint renders the configured server address.
func (p PostgresConfig) Endpoint() string {
	return net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
}

// ExecutionConfig describes the execution backend.
type ExecutionConfig struct {
	// Mode is "local" or "docker".
	Mode string
	// DockerTarget configures the docker backend.
	DockerTarget DockerTarget
	// Binaries names psql and pg_dump inside the execution context.
	Binaries pgclient.Binaries
}

// DockerTarget is the explicitly configured Docker execution target.
type DockerTarget struct {
	Container  string
	DockerPath string
	PgUser     string
}

// Defaults returns the configuration used when no flag is given.
func Defaults() Config {
	return Config{
		Listen:               "127.0.0.1:8080",
		Postgres:             PostgresConfig{Host: "127.0.0.1", Port: 5432, Database: "postgres", SSLMode: "prefer"},
		Execution:            ExecutionConfig{Mode: ExecutionLocal, Binaries: pgclient.DefaultBinaries(), DockerTarget: DockerTarget{DockerPath: "docker"}},
		ConnectTimeout:       10 * time.Second,
		QueryTimeout:         15 * time.Second,
		MetadataTimeout:      30 * time.Second,
		MetadataConcurrency:  4,
		MetadataMaxDatabases: 100,
		ShutdownTimeout:      10 * time.Second,
		ReadHeaderTimeout:    10 * time.Second,
		MaxConcurrentDumps:   4,
		MaxRequestBodyBytes:  64 << 10,
		LogLevel:             "info",
		LogFormat:            "text",
	}
}

// Parse builds a configuration from command line arguments.
//
// It returns flag.ErrHelp when -h/-help was requested; the caller should exit
// successfully in that case.
func Parse(args []string, stderr io.Writer) (Config, error) {
	cfg := Defaults()
	fs := flag.NewFlagSet("pg-dump-dashboard", flag.ContinueOnError)
	fs.SetOutput(stderr)

	showVersion := fs.Bool("version", false, "print version information and exit")

	fs.StringVar(&cfg.Listen, "listen", cfg.Listen, "HTTP listen address (host:port)")
	fs.StringVar(&cfg.Postgres.Host, "pg-host", cfg.Postgres.Host, "PostgreSQL server hostname or IP")
	fs.Var(&portFlag{port: &cfg.Postgres.Port}, "pg-port", "PostgreSQL server port")
	fs.StringVar(&cfg.Postgres.Database, "pg-database", cfg.Postgres.Database, "maintenance database used for discovery")
	fs.StringVar(&cfg.Postgres.SSLMode, "pg-sslmode", cfg.Postgres.SSLMode, "libpq sslmode: disable|allow|prefer|require|verify-ca|verify-full")

	fs.StringVar(&cfg.Execution.Mode, "execution", cfg.Execution.Mode, "execution backend: local|docker")
	fs.StringVar(&cfg.Execution.DockerTarget.Container, "docker-container", "", "explicit Docker container that runs psql and pg_dump (required for -execution=docker)")
	fs.StringVar(&cfg.Execution.DockerTarget.DockerPath, "docker-path", cfg.Execution.DockerTarget.DockerPath, "path to the Docker CLI executable")
	fs.StringVar(&cfg.Execution.DockerTarget.PgUser, "docker-pg-user", "", "run psql and pg_dump as this user inside the container (docker exec --user)")
	fs.StringVar(&cfg.Execution.Binaries.Psql, "psql-path", cfg.Execution.Binaries.Psql, "psql executable name or path inside the execution context")
	fs.StringVar(&cfg.Execution.Binaries.PgDump, "pg-dump-path", cfg.Execution.Binaries.PgDump, "pg_dump executable name or path inside the execution context")

	fs.DurationVar(&cfg.ConnectTimeout, "connect-timeout", cfg.ConnectTimeout, "libpq connection timeout")
	fs.DurationVar(&cfg.QueryTimeout, "query-timeout", cfg.QueryTimeout, "timeout for a single metadata query")
	fs.DurationVar(&cfg.MetadataTimeout, "metadata-timeout", cfg.MetadataTimeout, "timeout for a complete metadata refresh")
	fs.IntVar(&cfg.MetadataConcurrency, "metadata-concurrency", cfg.MetadataConcurrency, "number of databases inspected in parallel")
	fs.IntVar(&cfg.MetadataMaxDatabases, "metadata-max-databases", cfg.MetadataMaxDatabases, "maximum number of databases inspected per refresh")
	fs.DurationVar(&cfg.DumpTimeout, "dump-timeout", cfg.DumpTimeout, "maximum duration of a single dump download (0 = unlimited)")
	fs.IntVar(&cfg.MaxConcurrentDumps, "max-concurrent-dumps", cfg.MaxConcurrentDumps, "maximum number of simultaneous dump downloads")

	fs.DurationVar(&cfg.ShutdownTimeout, "shutdown-timeout", cfg.ShutdownTimeout, "graceful shutdown timeout")
	fs.DurationVar(&cfg.ReadHeaderTimeout, "read-header-timeout", cfg.ReadHeaderTimeout, "HTTP header read timeout")
	fs.Int64Var(&cfg.MaxRequestBodyBytes, "max-request-bytes", cfg.MaxRequestBodyBytes, "maximum size of a JSON request body")
	fs.BoolVar(&cfg.CookieSecure, "cookie-secure", cfg.CookieSecure, "mark the session cookie Secure (enable when serving over TLS)")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level: debug|info|warn|error")
	fs.StringVar(&cfg.LogFormat, "log-format", cfg.LogFormat, "log format: text|json")

	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintf(out, "%s\n\n", version.String())
		fmt.Fprintf(out, "Browse PostgreSQL databases and stream %s dumps to the browser.\n\n", "pg_dump")
		fmt.Fprintf(out, "Usage:\n  pg-dump-dashboard [flags]\n\nFlags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(out, "\nExample:\n  pg-dump-dashboard -pg-host db.internal -pg-port 5432 -listen :8080 -execution local\n")
	}

	if err := fs.Parse(args); err != nil {
		return Config{}, err //nolint:wrapcheck // flag errors are self-describing.
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}
	if *showVersion {
		fmt.Fprintln(stderr, version.String()) //nolint:errcheck // best effort on a CLI writer.
		return Config{}, flag.ErrHelp
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks the configuration for consistency.
func (c Config) Validate() error {
	var problems []error

	if err := validateListen(c.Listen); err != nil {
		problems = append(problems, err)
	}
	if strings.TrimSpace(c.Postgres.Host) == "" {
		problems = append(problems, errors.New("pg-host must not be empty"))
	}
	if c.Postgres.Port == 0 {
		problems = append(problems, errors.New("pg-port must be between 1 and 65535"))
	}
	if err := pgclient.ValidDatabaseName(c.Postgres.Database); err != nil {
		problems = append(problems, fmt.Errorf("pg-database: %w", err))
	}
	if !validSSLModes[c.Postgres.SSLMode] {
		problems = append(problems, fmt.Errorf("pg-sslmode %q is not a valid libpq sslmode", c.Postgres.SSLMode))
	}

	switch c.Execution.Mode {
	case ExecutionLocal:
	case ExecutionDocker:
		if c.Execution.DockerTarget.Container == "" {
			problems = append(problems, errors.New("execution=docker requires -docker-container"))
		}
		if c.Execution.DockerTarget.DockerPath == "" {
			problems = append(problems, errors.New("docker-path must not be empty"))
		}
	default:
		problems = append(problems, fmt.Errorf("execution %q is not supported (use local or docker)", c.Execution.Mode))
	}
	if err := c.Execution.Binaries.Validate(); err != nil {
		problems = append(problems, err)
	}

	if c.ConnectTimeout <= 0 {
		problems = append(problems, errors.New("connect-timeout must be positive"))
	}
	if c.QueryTimeout <= 0 {
		problems = append(problems, errors.New("query-timeout must be positive"))
	}
	if c.MetadataTimeout <= 0 {
		problems = append(problems, errors.New("metadata-timeout must be positive"))
	}
	if c.MetadataConcurrency < 1 || c.MetadataConcurrency > 32 {
		problems = append(problems, errors.New("metadata-concurrency must be between 1 and 32"))
	}
	if c.MetadataMaxDatabases < 0 {
		problems = append(problems, errors.New("metadata-max-databases must not be negative"))
	}
	if c.DumpTimeout < 0 {
		problems = append(problems, errors.New("dump-timeout must not be negative"))
	}
	if c.MaxConcurrentDumps < 1 || c.MaxConcurrentDumps > 64 {
		problems = append(problems, errors.New("max-concurrent-dumps must be between 1 and 64"))
	}
	if c.ShutdownTimeout <= 0 {
		problems = append(problems, errors.New("shutdown-timeout must be positive"))
	}
	if c.ReadHeaderTimeout <= 0 {
		problems = append(problems, errors.New("read-header-timeout must be positive"))
	}
	if c.MaxRequestBodyBytes < 1024 {
		problems = append(problems, errors.New("max-request-bytes must be at least 1024"))
	}
	if !validLogLevels[c.LogLevel] {
		problems = append(problems, fmt.Errorf("log-level %q must be debug, info, warn or error", c.LogLevel))
	}
	if !validLogFormats[c.LogFormat] {
		problems = append(problems, fmt.Errorf("log-format %q must be text or json", c.LogFormat))
	}

	return errors.Join(problems...)
}

// validateListen accepts ":8080", "127.0.0.1:8080" and "[::1]:8080".
func validateListen(addr string) error {
	if strings.TrimSpace(addr) == "" {
		return errors.New("listen address must not be empty")
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen address %q: %w", addr, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("listen address %q: invalid port", addr)
	}
	return nil
}

// portFlag adapts a *uint16 to flag.IntVar.
type portFlag struct{ port *uint16 }

func (p *portFlag) String() string { return strconv.Itoa(int(*p.port)) }

func (p *portFlag) Set(value string) error {
	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("invalid port %q: %w", value, err)
	}
	if n < 0 || n > 65535 {
		return fmt.Errorf("port %d out of range", n)
	}
	*p.port = uint16(n)
	return nil
}
