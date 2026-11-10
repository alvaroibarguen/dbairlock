// Package webui embeds the built dashboard into the binary.
//
// `make build` builds web/ with Vite and copies web/dist into
// internal/webui/dist before compiling. The directory is committed with only
// a .keep file (everything else in it is gitignored), so a plain `go build`
// also works: the binary then serves a short "not built" page instead of the
// dashboard.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built dashboard, rooted at its index.html.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// fs.Sub fails only for an invalid path; "dist" is a constant.
		panic(err)
	}
	return sub
}
