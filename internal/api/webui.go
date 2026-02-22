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
	mux.Handle("/", newWebUIHandler(distFS))
}

func newWebUIHandler(distFS fs.FS) http.Handler {
	fileServer := http.FileServerFS(distFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		assetPath := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if assetPath == "" || assetPath == "." {
			assetPath = "index.html"
		}

		if info, err := fs.Stat(distFS, assetPath); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
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
