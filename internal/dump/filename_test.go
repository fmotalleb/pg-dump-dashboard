package dump_test

import (
	"strings"
	"testing"
	"time"

	"github.com/fmotalleb/pg-dump-dashboard/internal/dump"
	"github.com/fmotalleb/pg-dump-dashboard/internal/execution/pgclient"
)

func TestSanitizeStem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain", input: "appdb", want: "appdb"},
		{name: "allowed punctuation", input: "app_db-2.1", want: "app_db-2.1"},
		{name: "path traversal", input: "../../etc/passwd", want: "etc_passwd"},
		{name: "windows separators", input: `C:\data\db`, want: "C__data_db"},
		{name: "quotes and newlines", input: "a\"; rm -rf /\n", want: "a___rm_-rf"},
		{name: "header injection attempt", input: "db\r\nX-Evil: 1", want: "db__X-Evil__1"},
		{name: "unicode", input: "dätäbäse", want: "d_t_b_se"},
		{name: "empty", input: "", want: "database"},
		{name: "only punctuation", input: "...", want: "database"},
		{name: "leading dots", input: ".hidden", want: "hidden"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := dump.SanitizeStem(tc.input); got != tc.want {
				t.Fatalf("SanitizeStem(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSanitizeStemBoundsLength(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 500)
	got := dump.SanitizeStem(long)
	if len(got) > 96 {
		t.Fatalf("sanitised stem is %d bytes, want <= 96", len(got))
	}
}

func TestFilenameIsSafeAndPredictable(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 3, 4, 5, 6, 7, 0, time.UTC)

	got := dump.Filename("appdb", pgclient.FormatCustom, now)
	if want := "appdb-20250304T050607Z.dump"; got != want {
		t.Fatalf("Filename() = %q, want %q", got, want)
	}

	got = dump.Filename("app db/../x", pgclient.FormatPlain, now)
	if want := "app_db___x-20250304T050607Z.sql"; got != want {
		t.Fatalf("Filename() = %q, want %q", got, want)
	}

	for _, format := range []pgclient.DumpFormat{pgclient.FormatCustom, pgclient.FormatPlain} {
		name := dump.Filename("../evil\r\n", format, now)
		if strings.ContainsAny(name, `/\`+"\r\n\"") {
			t.Fatalf("unsafe filename produced: %q", name)
		}
	}
}

func TestContentDisposition(t *testing.T) {
	t.Parallel()

	header := dump.ContentDisposition("appdb-20250101T000000Z.dump")
	if !strings.HasPrefix(header, "attachment;") {
		t.Fatalf("expected an attachment header: %s", header)
	}
	if !strings.Contains(header, `filename="appdb-20250101T000000Z.dump"`) {
		t.Fatalf("missing quoted filename: %s", header)
	}
	if !strings.Contains(header, "filename*=utf-8''appdb-20250101T000000Z.dump") {
		t.Fatalf("missing RFC 5987 filename: %s", header)
	}
	if strings.ContainsAny(header, "\r\n") {
		t.Fatal("header contains CR/LF")
	}
}

func TestContentType(t *testing.T) {
	t.Parallel()

	if got := dump.ContentType(pgclient.FormatPlain); !strings.HasPrefix(got, "application/sql") {
		t.Fatalf("plain content type = %q", got)
	}
	if got := dump.ContentType(pgclient.FormatCustom); got != "application/octet-stream" {
		t.Fatalf("custom content type = %q", got)
	}
}
