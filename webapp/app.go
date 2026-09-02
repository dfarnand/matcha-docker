package main

import (
	"html/template"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gomarkdown/markdown"
)

var outputDir = envOr("MATCHA_OUTPUT_DIR", "/app/output")

type FileInfo struct {
	Name   string
	Date   string
	Active bool
}

type PageData struct {
	Files   []FileInfo
	Content template.HTML
}

// safeMarkdownName rejects anything that is not a plain digest filename.
//
// The name comes straight from the URL and is joined onto outputDir. Now that
// the config directory holds auth.json and an API key, a traversal here would
// be credential disclosure rather than a curiosity, so this is checked at both
// the handler and the read.
func safeMarkdownName(name string) bool {
	if name == "" || name != path.Base(name) {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return false
	}
	return strings.HasSuffix(name, ".md")
}

func listMarkdownFiles() ([]FileInfo, error) {
	var files []FileInfo

	entries, err := os.ReadDir(outputDir)
	if err != nil {
		if os.IsNotExist(err) {
			return files, nil
		}
		return nil, err
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		files = append(files, FileInfo{Name: e.Name(), Date: name})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Date > files[j].Date
	})

	return files, nil
}

func getLatestFile() (string, error) {
	files, err := listMarkdownFiles()
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", nil
	}
	return files[0].Name, nil
}

func renderMarkdown(filename string) (template.HTML, error) {
	if !safeMarkdownName(filename) {
		return "", os.ErrNotExist
	}
	path := filepath.Join(outputDir, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	html := markdown.ToHTML(data, nil, nil)
	return template.HTML(html), nil
}

func renderPage(w http.ResponseWriter, filename string) {
	files, err := listMarkdownFiles()
	if err != nil {
		http.Error(w, "Error reading files", http.StatusInternalServerError)
		return
	}

	if filename == "" {
		latest, err := getLatestFile()
		if err != nil || latest == "" {
			render(w, "index.html", PageData{Files: files})
			return
		}
		filename = latest
	} else if !safeMarkdownName(filename) {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	for i := range files {
		files[i].Active = files[i].Name == filename
	}

	content, err := renderMarkdown(filename)
	if err != nil {
		http.Error(w, "Error reading file", http.StatusNotFound)
		return
	}

	render(w, "index.html", PageData{Files: files, Content: content})
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	renderPage(w, "")
}

func handleFile(w http.ResponseWriter, r *http.Request) {
	renderPage(w, r.PathValue("name"))
}

type fileEntry struct {
	Name string `json:"name"`
	Date string `json:"date"`
}

func filesHandler(w http.ResponseWriter, r *http.Request) {
	files, err := listMarkdownFiles()
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	entries := make([]fileEntry, 0, len(files))
	for _, f := range files {
		entries = append(entries, fileEntry{Name: f.Name, Date: f.Date})
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": entries})
}
