package main

import (
	"bytes"
	"embed"
	"html/template"
	"log"
	"net/http"
)

//go:embed templates
var templateFS embed.FS

// Each page gets its own template set. A single set cannot work here because
// every page defines the same block names ("body", "sidebar", ...) and they
// would collide.
var pages = map[string]*template.Template{}

func init() {
	for _, name := range []string{"index.html", "login.html", "settings.html"} {
		pages[name] = template.Must(template.ParseFS(
			templateFS, "templates/layout.html", "templates/"+name))
	}
}

// render writes a page, buffering first so that a template error produces a
// clean 500 instead of a half-written page served with a 200.
func render(w http.ResponseWriter, name string, data any) {
	tmpl, ok := pages[name]
	if !ok {
		log.Printf("render: no such template %q", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := buf.WriteTo(w); err != nil {
		log.Printf("write %s: %v", name, err)
	}
}
