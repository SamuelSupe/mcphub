package app

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed adminui/*
var adminAssets embed.FS

func (a *App) serveAdminUI(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	assetPath := strings.TrimPrefix(path.Clean(req.URL.Path), "/")
	if assetPath == "." || assetPath == "" {
		assetPath = "index.html"
	}
	assets, _ := fs.Sub(adminAssets, "adminui")
	if _, err := fs.Stat(assets, assetPath); err != nil {
		http.NotFound(w, req)
		return
	}
	if assetPath == "index.html" {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.FileServer(http.FS(assets)).ServeHTTP(w, req)
}
