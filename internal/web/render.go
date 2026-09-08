package web

import (
	"bytes"
	"html/template"
	"log/slog"
	"net/http"
)

// Finish rendering before committing headers or body so template failures
// cannot leave a successful response containing only part of a page.
func renderTemplate(w http.ResponseWriter, tmpl *template.Template, name string, status int, d *pageData) {
	var body bytes.Buffer
	if err := tmpl.ExecuteTemplate(&body, name, d); err != nil {
		slog.Error("template rendering failed", "template", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := body.WriteTo(w); err != nil {
		slog.Error("template response write failed", "template", name, "error", err)
	}
}
