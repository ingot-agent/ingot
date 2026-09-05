package appcomponent

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// webFiles is built from web/app-webui and committed so Go consumers do not
// need a JavaScript toolchain.
//
//go:embed webdist
var webFiles embed.FS

func webHandler() http.Handler {
	files, err := fs.Sub(webFiles, "webdist")
	if err != nil {
		panic(err)
	}
	server := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		info, err := fs.Stat(files, name)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' blob: data:; media-src 'self' blob:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			noCache(w)
		}
		server.ServeHTTP(w, r)
	})
}
