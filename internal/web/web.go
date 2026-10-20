// Package web embeds the compiled frontend.
//
// The assets under dist/ are produced by `npm --prefix web run build` (which
// runs Tailwind CSS and copies the sources) before `go build`. A placeholder
// file is committed so the package still compiles on a clean checkout; the
// placeholder is replaced by the real index.html during the frontend build.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embedded embed.FS

// Dist returns the embedded frontend rooted at dist/.
func Dist() (fs.FS, error) {
	return fs.Sub(embedded, "dist")
}

// HasAssets reports whether a compiled index.html is embedded. When it is not,
// the binary was built without running the frontend build.
func HasAssets(dist fs.FS) bool {
	info, err := fs.Stat(dist, "index.html")
	return err == nil && !info.IsDir()
}
