package postgres

import (
	"fmt"
	"strconv"
	"strings"
)

// row is one parsed psql output row.
type row []string

// parseRows splits psql's unaligned output into tagged rows.
func parseRows(output string) []row {
	var rows []row
	for line := range strings.Lines(output) {
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		rows = append(rows, strings.Split(line, fieldSep))
	}
	return rows
}

func at(row row, i int) string {
	if i < len(row) {
		return row[i]
	}
	return ""
}

func atInt(row row, i int) int {
	n, _ := strconv.Atoi(strings.TrimSpace(at(row, i)))
	return n
}

func atInt64(row row, i int) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(at(row, i)), 10, 64)
	return n
}

func atBool(row row, i int) bool {
	switch strings.TrimSpace(at(row, i)) {
	case "t", "true", "on", "1":
		return true
	default:
		return false
	}
}

func requireFields(row row, n int, tag string) error {
	if len(row) < n {
		return fmt.Errorf("malformed %q row: got %d fields, want %d", tag, len(row), n)
	}
	return nil
}

// serverName is one database found by the server query.
type serverName struct {
	name             string
	owner            string
	sizeBytes        int64
	encoding         string
	collate          string
	ctype            string
	allowConnections bool
	connectPrivilege bool
	connections      int
}

// parseServerResult extracts the server version and the database list.
func parseServerResult(output string) (version, currentUser, currentDB string, dbs []serverName, err error) {
	for _, r := range parseRows(output) {
		switch at(r, 0) {
		case "srv":
			if e := requireFields(r, 4, "srv"); e != nil {
				return "", "", "", nil, e
			}
			version = at(r, 1)
			currentUser = at(r, 2)
			currentDB = at(r, 3)
		case "db":
			if e := requireFields(r, 10, "db"); e != nil {
				return "", "", "", nil, e
			}
			dbs = append(dbs, serverName{
				name:             at(r, 1),
				owner:            at(r, 2),
				sizeBytes:        atInt64(r, 3),
				encoding:         at(r, 4),
				collate:          at(r, 5),
				ctype:            at(r, 6),
				allowConnections: atBool(r, 7),
				connectPrivilege: atBool(r, 8),
				connections:      atInt(r, 9),
			})
		}
	}
	if version == "" {
		return "", "", "", nil, fmt.Errorf("server query returned no version information")
	}
	return version, currentUser, currentDB, dbs, nil
}

// databaseStats is the per-database part of the discovery result.
type databaseStats struct {
	counts  ObjectCounts
	rows    int64
	tables  []TableStat
	hasRows bool
}

// parseDatabaseResult extracts catalog counts and relation statistics.
func parseDatabaseResult(output string) (databaseStats, error) {
	var stats databaseStats

	for _, r := range parseRows(output) {
		switch at(r, 0) {
		case "schema":
			if err := requireFields(r, 2, "schema"); err != nil {
				return stats, err
			}
			stats.counts.Schemas = atInt(r, 1)
		case "obj":
			if err := requireFields(r, 3, "obj"); err != nil {
				return stats, err
			}
			n := atInt(r, 2)
			switch at(r, 1) {
			case "r":
				stats.counts.Tables += n
			case "p":
				stats.counts.Tables += n
			case "v":
				stats.counts.Views += n
			case "m":
				stats.counts.Views += n
				stats.counts.MaterializedViews += n
			case "S":
				stats.counts.Sequences += n
			case "i", "I":
				stats.counts.Indexes += n
			}
		case "fn":
			if err := requireFields(r, 2, "fn"); err != nil {
				return stats, err
			}
			stats.counts.Functions = atInt(r, 1)
		case "rows":
			if err := requireFields(r, 3, "rows"); err != nil {
				return stats, err
			}
			stats.rows = atInt64(r, 1)
			stats.hasRows = true
		case "table":
			if err := requireFields(r, 5, "table"); err != nil {
				return stats, err
			}
			stats.tables = append(stats.tables, TableStat{
				Schema:          at(r, 1),
				Name:            at(r, 2),
				Rows:            atInt64(r, 3),
				RowsAreEstimate: true,
				SizeBytes:       atInt64(r, 4),
			})
		}
	}
	return stats, nil
}
