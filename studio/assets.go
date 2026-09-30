package studio

import (
	"embed"
	"io/fs"
)

//go:embed all:web/dist
var webDist embed.FS

// WebAssets is the built web app (web/dist), rooted at index.html. The
// frontend build overwrites web/dist; a placeholder page is kept there so
// the package always compiles.
func WebAssets() fs.FS {
	sub, err := fs.Sub(webDist, "web/dist")
	if err != nil {
		return webDist
	}
	return sub
}
