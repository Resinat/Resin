package api

import (
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"

	embeddedwebui "github.com/Resinat/Resin/webui"
)

func registerEmbeddedWebUI(mux *http.ServeMux) {
	distFS, err := embeddedwebui.DistFS()
	if err != nil {
		log.Printf("WebUI embed disabled: %v", err)
		return
	}
	mux.Handle("/", newRootRedirectHandler())
	mux.Handle("/ui", newUIRootRedirectHandler())
	mux.Handle("/ui/", newWebUIHandler(distFS))
}

func newWebUIHandler(distFS fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		if !strings.HasPrefix(r.URL.Path, "/ui/") {
			http.NotFound(w, r)
			return
		}

		assetPath := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, "/ui/")), "/")
		if assetPath == "" || assetPath == "." {
			assetPath = "index.html"
		}

		if info, err := fs.Stat(distFS, assetPath); err == nil && !info.IsDir() {
			http.ServeFileFS(w, r, distFS, assetPath)
			return
		}

		// Missing requests with file-like paths should remain 404.
		if path.Ext(assetPath) != "" {
			http.NotFound(w, r)
			return
		}

		http.ServeFileFS(w, r, distFS, "index.html")
	})
}

func newRootRedirectHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})
}

func newUIRootRedirectHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ui" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/ui/", http.StatusFound)
	})
}
