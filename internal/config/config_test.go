package config_test

import (
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fmotalleb/pg-dump-dashboard/internal/config"
)

func TestDefaultsAreValid(t *testing.T) {
	cfg := config.Defaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default configuration must be valid: %v", err)
	}
	if cfg.Execution.Mode != config.ExecutionLocal {
		t.Fatalf("default execution mode = %q, want local", cfg.Execution.Mode)
	}
	if cfg.Execution.Binaries.Psql != "psql" || cfg.Execution.Binaries.PgDump != "pg_dump" {
		t.Fatalf("unexpected default binaries: %+v", cfg.Execution.Binaries)
	}
}

func TestParseFlags(t *testing.T) {
	t.Parallel()

	cfg, err := config.Parse([]string{
		"-listen", ":9000",
		"-pg-host", "db.internal",
		"-pg-port", "6543",
		"-pg-database", "maintenance",
		"-pg-sslmode", "require",
		"-execution", "docker",
		"-docker-container", "postgres-db",
		"-docker-path", "/usr/local/bin/docker",
		"-docker-pg-user", "postgres",
		"-metadata-concurrency", "2",
		"-dump-timeout", "90m",
		"-log-format", "json",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if cfg.Listen != ":9000" {
		t.Errorf("listen = %q", cfg.Listen)
	}
	if cfg.Postgres.Host != "db.internal" || cfg.Postgres.Port != 6543 {
		t.Errorf("postgres endpoint = %s:%d", cfg.Postgres.Host, cfg.Postgres.Port)
	}
	if got, want := cfg.Postgres.Endpoint(), "db.internal:6543"; got != want {
		t.Errorf("endpoint = %q, want %q", got, want)
	}
	if cfg.Postgres.SSLMode != "require" || cfg.Postgres.Database != "maintenance" {
		t.Errorf("unexpected postgres config: %+v", cfg.Postgres)
	}
	if cfg.Execution.Mode != config.ExecutionDocker || cfg.Execution.DockerTarget.Container != "postgres-db" {
		t.Errorf("unexpected execution config: %+v", cfg.Execution)
	}
	if cfg.Execution.DockerTarget.DockerPath != "/usr/local/bin/docker" {
		t.Errorf("docker path = %q", cfg.Execution.DockerTarget.DockerPath)
	}
	if cfg.Execution.DockerTarget.PgUser != "postgres" {
		t.Errorf("docker user = %q", cfg.Execution.DockerTarget.PgUser)
	}
	if cfg.MetadataConcurrency != 2 {
		t.Errorf("concurrency = %d", cfg.MetadataConcurrency)
	}
	if cfg.DumpTimeout != 90*time.Minute {
		t.Errorf("dump timeout = %v", cfg.DumpTimeout)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("log format = %q", cfg.LogFormat)
	}
}

func TestParseRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"invalid port", []string{"-pg-port", "70000"}, "port"},
		{"non numeric port", []string{"-pg-port", "abc"}, "invalid port"},
		{"missing host", []string{"-pg-host", ""}, "pg-host"},
		{"bad sslmode", []string{"-pg-sslmode", "sometimes"}, "pg-sslmode"},
		{"unknown execution", []string{"-execution", "ssh"}, "not supported"},
		{"docker without container", []string{"-execution", "docker"}, "docker-container"},
		{"bad listen", []string{"-listen", "nonsense"}, "listen address"},
		{"bad database name", []string{"-pg-database", "bad;name"}, "pg-database"},
		{"negative concurrency", []string{"-metadata-concurrency", "0"}, "metadata-concurrency"},
		{"negative dump timeout", []string{"-dump-timeout", "-1s"}, "dump-timeout"},
		{"bad log level", []string{"-log-level", "loud"}, "log-level"},
		{"positional args", []string{"oops"}, "unexpected positional"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.Parse(tc.args, io.Discard)
			if err == nil {
				t.Fatalf("expected an error for %v", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestParseHelpAndVersion(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	_, err := config.Parse([]string{"-h"}, &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected flag.ErrHelp, got %v", err)
	}
	usage := out.String()
	for _, flagName := range []string{"-pg-host", "-pg-port", "-listen", "-execution", "-docker-container"} {
		if !strings.Contains(usage, flagName) {
			t.Errorf("usage output does not document %s", flagName)
		}
	}

	out.Reset()
	_, err = config.Parse([]string{"-version"}, &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected flag.ErrHelp for -version, got %v", err)
	}
	if !strings.Contains(out.String(), "pg-dump-dashboard") {
		t.Errorf("version output missing name: %q", out.String())
	}
}

func TestValidateListenAddresses(t *testing.T) {
	t.Parallel()

	valid := []string{":8080", "127.0.0.1:8080", "[::1]:8080", "0.0.0.0:80"}
	for _, addr := range valid {
		cfg := config.Defaults()
		cfg.Listen = addr
		if err := cfg.Validate(); err != nil {
			t.Errorf("listen %q should be valid: %v", addr, err)
		}
	}
}
