package web_test

import (
	"io/fs"
	"testing"

	"github.com/fmotalleb/pg-dump-dashboard/internal/web"
)

func TestDistReturnsFrontendRoot(t *testing.T) {
	t.Parallel()

	dist, err := web.Dist()
	if err != nil {
		t.Fatalf("Dist: %v", err)
	}

	// The directory always exists; index.html is only present after
	// `make frontend`. Either state must be reported consistently.
	hasAssets := web.HasAssets(dist)

	_, statErr := fs.Stat(dist, "index.html")
	if hasAssets && statErr != nil {
		t.Fatalf("HasAssets reported true but index.html is missing: %v", statErr)
	}
	if !hasAssets && statErr == nil {
		t.Fatal("HasAssets reported false but index.html exists")
	}

	if hasAssets {
		if _, err := fs.Stat(dist, "assets/app.css"); err != nil {
			t.Fatalf("the compiled stylesheet is missing: %v", err)
		}
	}
}
