package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// SPAHandler serves a single-page app from fsys.
//
// Requests for files that exist are served directly. Any other path returns
// the index document so client-side routes survive a page refresh. Files
// under assets/ have content-hashed names and are cached forever; the index
// document is never cached. index is the entry document's name in fsys.
func SPAHandler(fsys fs.FS, index string) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != index {
			if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				files.ServeHTTP(w, r)
				return
			}
		}

		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, fsys, index)
	})
}
