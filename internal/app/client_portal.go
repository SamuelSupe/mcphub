package app

import (
	"embed"
	"net/http"
)

//go:embed clientportal/*
var clientPortal embed.FS

func (a *App) serveClientPortal(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "Method not allowed", 405)
		return
	}
	var file, contentType string
	switch req.URL.Path {
	case "/client-auth/":
		file, contentType = "index.html", "text/html; charset=utf-8"
	case "/client-auth/accounts.js":
		file, contentType = "accounts.js", "text/javascript; charset=utf-8"
	case "/client-auth/app.js":
		file, contentType = "app.js", "text/javascript; charset=utf-8"
	case "/client-auth/styles.css":
		file, contentType = "styles.css", "text/css; charset=utf-8"
	default:
		http.NotFound(w, req)
		return
	}
	data, err := clientPortal.ReadFile("clientportal/" + file)
	if err != nil {
		http.Error(w, "Portal unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(data)
}
