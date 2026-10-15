// Package web embeds the built React app. Run `npm run build` in web/ before `go build`.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built UI rooted at dist/.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // only fails on an invalid pattern, which is a compile-time constant
	}
	return sub
}
