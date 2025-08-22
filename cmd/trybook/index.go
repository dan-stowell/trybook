package main

import (
	"embed"
	"html/template"
	"net/http"
)

	//go:embed index.html
	var indexFS embed.FS

	var indexTmpl = template.Must(template.ParseFS(indexFS, "index.html"))

func indexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTmpl.Execute(w, nil); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}
