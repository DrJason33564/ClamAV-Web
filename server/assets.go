package main

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
)

//go:embed web/index.html web/static/*
var webAssets embed.FS

var staticAssets = mustSubFS(webAssets, "web/static")

func mustSubFS(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = indexTemplate.Execute(w, map[string]any{"Roots": s.cfg.BrowseRoots})
}

var indexTemplate = template.Must(template.ParseFS(webAssets, "web/index.html"))
