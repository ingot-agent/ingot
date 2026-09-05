package appcomponent

import (
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedWebUIRoutesAndCaching(t *testing.T) {
	a := testApplication(t)
	var assetPath string
	if err := fs.WalkDir(webFiles, "webdist/assets", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".js") {
			assetPath = strings.TrimPrefix(path, "webdist")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if assetPath == "" {
		t.Fatal("embedded JavaScript is missing")
	}
	for _, test := range []struct {
		method, path   string
		status         int
		content, cache string
	}{
		{"GET", "/", 200, "<!doctype html>", "no-store"},
		{"GET", "/index.html", 301, "", "no-store"},
		{"GET", assetPath, 200, "", "public, max-age=31536000, immutable"},
		{"HEAD", "/", 200, "", "no-store"},
		{"POST", "/", 405, "method not allowed", ""},
		{"GET", "/assets/missing.js", 404, "404", ""},
		{"GET", "/assets/", 404, "404", ""},
		{"GET", "/api/missing", 404, "API endpoint not found", ""},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			a.routes().ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))
			if w.Code != test.status || !strings.Contains(w.Body.String(), test.content) {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != test.cache {
				t.Fatalf("cache = %q", w.Header().Get("Cache-Control"))
			}
			if test.path == "/" && test.method == "GET" && !strings.Contains(w.Header().Get("Content-Security-Policy"), "object-src 'none'") {
				t.Fatal("missing content policy")
			}
			if test.path == "/api/missing" && w.Header().Get("Content-Type") != "application/json" {
				t.Fatal("API fallback served HTML")
			}
		})
	}
}
