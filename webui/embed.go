package webui

import (
	"embed"
	"io/fs"
)

// distFS contains the compiled WebUI assets under webui/dist.
//
//go:embed dist
var distFS embed.FS

// DistFS returns an fs.FS rooted at the embedded dist directory.
func DistFS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
