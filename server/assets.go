package main

import (
	"embed"
	"io/fs"
	"net/http"
)

// Embed the web directory rather than only dist so the Go package remains
// buildable before the frontend build has run. Production images contain the
// Vite-generated web/dist directory.
//
//go:embed web
var webAssets embed.FS

var distAssets = mustSubFS(webAssets, "web/dist")

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
