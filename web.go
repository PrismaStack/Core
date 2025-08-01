package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// serveWebApp returns an http.Handler that serves static files from the "web" directory.
// If the file is not found, it serves index.html (for Flutter web SPA routing).
func serveWebApp() http.Handler {
	webDir := "web"
	fs := http.FileServer(http.Dir(webDir))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prevent API or uploads from being handled here.
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/uploads/") {
			http.NotFound(w, r)
			return
		}

		requestedPath := filepath.Join(webDir, filepath.Clean(r.URL.Path))
		stat, err := os.Stat(requestedPath)
		if err == nil && !stat.IsDir() {
			// File exists, serve it
			fs.ServeHTTP(w, r)
			return
		}

		// If file doesn't exist, serve index.html for Flutter web SPA routing
		indexFile := filepath.Join(webDir, "index.html")
		http.ServeFile(w, r, indexFile)
	})
}