package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gomarkdown/markdown"
)

const outputDir = "/app/output"

var indexTemplate = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Matcha Digest</title>
    <link rel="icon" href="data:image/svg+xml,<svg xmlns=%22http://www.w3.org/2000/svg%22 viewBox=%220 0 100 100%22><text y=%22.9em%22 font-size=%2290%22>🍵</text></svg>">
    <link rel="stylesheet" href="/static/style.css">
</head>
<body>
    <input type="checkbox" id="menu-toggle" class="menu-toggle">
    <label for="menu-toggle" class="menu-btn">☰</label>
    <nav class="sidebar">
        <h2>Files</h2>
        <ul class="file-list">
            {{range .Files}}
            <li><a href="/file/{{.Name}}" {{if .Active}}class="active"{{end}}>{{.Date}}</a></li>
            {{end}}
        </ul>
    </nav>
    <main class="content">
        {{.Content}}
    </main>
</body>
</html>
`))

type FileInfo struct {
	Name    string
	Date    string
	Active  bool
}

type PageData struct {
	Files   []FileInfo
	Content template.HTML
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
		http.Error(w, "Error reading files", 500)
		return
	}

	if filename == "" {
		latest, err := getLatestFile()
		if err != nil || latest == "" {
			indexTemplate.Execute(w, PageData{Files: files})
			return
		}
		filename = latest
	}

	for i := range files {
		files[i].Active = files[i].Name == filename
	}

	content, err := renderMarkdown(filename)
	if err != nil {
		http.Error(w, "Error reading file", 404)
		return
	}

	indexTemplate.Execute(w, PageData{Files: files, Content: content})
}

func filesHandler(w http.ResponseWriter, r *http.Request) {
	files, err := listMarkdownFiles()
	if err != nil {
		http.Error(w, "Error", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, "{\"files\":[")
	for i, f := range files {
		if i > 0 {
			fmt.Fprintf(w, ",")
		}
		fmt.Fprintf(w, "{\"name\":\"%s\",\"date\":\"%s\"}", f.Name, f.Date)
	}
	fmt.Fprintf(w, "]}")
}

func main() {
	fs := http.FileServer(http.Dir("/app/webapp/static"))
	http.Handle("/static/", http.StripPrefix("/static/", fs))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/files" {
			filesHandler(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/file/") {
			filename := strings.TrimPrefix(r.URL.Path, "/file/")
			renderPage(w, filename)
			return
		}
		renderPage(w, "")
	})

	log.Println("Server starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
