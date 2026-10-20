// Package version exposes build metadata injected by the linker.
package version

import "fmt"

// Values are overridden at build time:
//
//	-X github.com/fmotalleb/pg-dump-dashboard/internal/version.Version=1.2.3
//	-X github.com/fmotalleb/pg-dump-dashboard/internal/version.Commit=abc1234
//	-X github.com/fmotalleb/pg-dump-dashboard/internal/version.Date=2025-01-01T00:00:00Z
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String renders the full version line printed by --version.
func String() string {
	return fmt.Sprintf("pg-dump-dashboard %s (commit %s, built %s)", Version, Commit, Date)
}
