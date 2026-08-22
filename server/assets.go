package main

import (
	"embed"
	"io/fs"
	"net/http"
)

// Embed the generated web directory. The repository keeps only a web-dist
// placeholder; Docker replaces it with the Vite-generated frontend assets.
//
//go:embed web-dist
var webAssets embed.FS

var distAssets = mustSubFS(webAssets, "web-dist")

func mustSubFS(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

func frontendHandler() http.Handler {
	return http.FileServer(http.FS(distAssets))
}
