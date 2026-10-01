// Package web embeds the built React UI so pgdock-server ships as one binary.
//
// The embed directive cannot reference parent directories, so this package embeds its
// own dist folder and exports it as an fs.FS for the server to serve.
//
// dist/placeholder.html is committed so backend-only work compiles without
// Node installed. `make build` writes the real UI (dist/index.html and
// dist/assets/) next to it; everything else in dist is gitignored.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// PlaceholderFile is served at / when no UI build is embedded.
const PlaceholderFile = "placeholder.html"

// IndexFile is the entry point of a real UI build.
const IndexFile = "index.html"

// Dist returns the embedded UI build output.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed pattern guarantees dist exists
	}
	return sub
}

// HasUI reports whether a real UI build (not just the placeholder) is
// embedded. Release builds must return true.
func HasUI() bool {
	_, err := fs.Stat(Dist(), IndexFile)
	return err == nil
}
