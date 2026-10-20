package httpapi

import (
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

// staticHandler serves the embedded frontend.
//
// Unknown paths fall back to index.html so the dashboard works with a plain
// deep link, while /api/ paths are handled by the API router.
func (h *Handler) staticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, codeInvalidRequest, "method not allowed")
			return
		}
		if h.static == nil {
			writeError(w, http.StatusNotFound, codeNotFound, "frontend assets are not embedded in this build")
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}

		file, err := h.static.Open(name)
		if err != nil {
			name = "index.html"
			file, err = h.static.Open(name)
			if err != nil {
				writeError(w, http.StatusInternalServerError, codeInternal,
					"frontend assets are missing from this build: run `make frontend` and rebuild")
				return
			}
		}
		defer func() { _ = file.Close() }()

		info, err := file.Stat()
		if err != nil || info.IsDir() {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return
		}

		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}

		seeker, ok := file.(io.ReadSeeker)
		if !ok {
			w.WriteHeader(http.StatusOK)
			_, _ = io.Copy(w, file)
			return
		}
		http.ServeContent(w, r, name, time.Time{}, seeker)
	})
}
