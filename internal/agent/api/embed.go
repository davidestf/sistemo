package api

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dashboard_dist
var dashboardFS embed.FS

// DashboardHandler returns an http.HandlerFunc that serves the embedded SPA.
// Static files (JS, CSS, images) are served with immutable cache headers.
// index.html is served with no-cache so updates take effect immediately.
// All non-file paths fall back to index.html for client-side hash routing.
func DashboardHandler() http.HandlerFunc {
	sub, _ := fs.Sub(dashboardFS, "dashboard_dist")
	indexHTML, _ := fs.ReadFile(sub, "index.html")

	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/dashboard")
		path = strings.TrimPrefix(path, "/")

		// Root → serve index.html with no-cache (updates take effect on refresh)
		if path == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			_, _ = w.Write(indexHTML)
			return
		}

		// Try to serve a static file (JS/CSS/fonts have hashed names → immutable cache)
		if f, err := sub.(fs.ReadFileFS).ReadFile(path); err == nil {
			switch {
			case strings.HasSuffix(path, ".js"):
				w.Header().Set("Content-Type", "application/javascript")
			case strings.HasSuffix(path, ".css"):
				w.Header().Set("Content-Type", "text/css")
			case strings.HasSuffix(path, ".svg"):
				w.Header().Set("Content-Type", "image/svg+xml")
			case strings.HasSuffix(path, ".html"):
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
			case strings.HasSuffix(path, ".png"):
				w.Header().Set("Content-Type", "image/png")
			case strings.HasSuffix(path, ".jpg"), strings.HasSuffix(path, ".jpeg"):
				w.Header().Set("Content-Type", "image/jpeg")
			case strings.HasSuffix(path, ".woff2"):
				w.Header().Set("Content-Type", "font/woff2")
			case strings.HasSuffix(path, ".woff"):
				w.Header().Set("Content-Type", "font/woff")
			case strings.HasSuffix(path, ".ttf"):
				w.Header().Set("Content-Type", "font/ttf")
			case strings.HasSuffix(path, ".json"):
				w.Header().Set("Content-Type", "application/json")
			}
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			_, _ = w.Write(f)
			return
		}

		// SPA fallback: serve index.html for all non-file routes
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		_, _ = w.Write(indexHTML)
	}
}
