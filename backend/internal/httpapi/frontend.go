package httpapi

import (
	"net/http"
	"path/filepath"

	"github.com/go-chi/chi/v5"
)

func mountFrontend(r chi.Router, dir string) {
	serveHTML := func(file string) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, req, filepath.Join(dir, file))
		}
	}

	assets := http.StripPrefix("/assets/", http.FileServer(http.Dir(filepath.Join(dir, "assets"))))
	serveAsset := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		assets.ServeHTTP(w, req)
	})

	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, req, "/admin", http.StatusTemporaryRedirect)
	})
	r.Get("/polls/*", serveHTML("index.html"))
	r.Head("/polls/*", serveHTML("index.html"))
	r.Get("/admin", serveHTML(filepath.Join("admin", "index.html")))
	r.Head("/admin", serveHTML(filepath.Join("admin", "index.html")))
	r.Get("/admin/*", serveHTML(filepath.Join("admin", "index.html")))
	r.Head("/admin/*", serveHTML(filepath.Join("admin", "index.html")))
	r.Method(http.MethodGet, "/assets/*", serveAsset)
	r.Method(http.MethodHead, "/assets/*", serveAsset)
}
