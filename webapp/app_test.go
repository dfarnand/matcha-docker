package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The filename comes straight from the URL and is joined onto outputDir. Now
// that /app/config holds auth.json and an API key, a traversal here would be
// credential disclosure.
func TestSafeMarkdownName(t *testing.T) {
	valid := []string{"2026-09-02.md", "prefix-2026-09-02-suffix.md"}
	for _, n := range valid {
		if !safeMarkdownName(n) {
			t.Errorf("safeMarkdownName(%q) = false, want true", n)
		}
	}

	invalid := []string{
		"",
		"../config/auth.json",
		"../../config/settings.json",
		"/etc/passwd",
		`..\config\auth.json`,
		".hidden.md",
		"notmarkdown.txt",
		"sub/dir/file.md",
		"auth.json",
	}
	for _, n := range invalid {
		if safeMarkdownName(n) {
			t.Errorf("safeMarkdownName(%q) = true, want false", n)
		}
	}
}

func TestRenderMarkdownRefusesTraversal(t *testing.T) {
	if _, err := renderMarkdown("../config/auth.json"); err == nil {
		t.Error("renderMarkdown should refuse a traversing path")
	}
	if _, err := renderMarkdown("auth.json"); err == nil {
		t.Error("renderMarkdown should refuse a non-markdown file")
	}
}

func TestHandleFileRejectsTraversal(t *testing.T) {
	// The mux decodes %2f before routing, so exercise the handler directly
	// with the value a crafted request would produce.
	r := httptest.NewRequest("GET", "/file/x", nil)
	r.SetPathValue("name", "../config/auth.json")
	w := httptest.NewRecorder()

	handleFile(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if strings.Contains(w.Body.String(), "password_hash") {
		t.Error("handler leaked file contents")
	}
}

func TestFilesHandlerEmitsValidJSON(t *testing.T) {
	w := httptest.NewRecorder()
	filesHandler(w, httptest.NewRequest("GET", "/files", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
	// The previous hand-rolled version emitted invalid JSON for names
	// containing quotes or backslashes.
	if !strings.HasPrefix(strings.TrimSpace(w.Body.String()), `{"files":`) {
		t.Errorf("unexpected body: %s", w.Body.String())
	}
}

func TestWriteFileAtomicPermissionsAndReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "thing.json")

	if err := writeFileAtomic(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}

	if err := writeFileAtomic(path, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Errorf("content = %q, want %q", data, "second")
	}

	// No temp files should be left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestTailTrimsToLimit(t *testing.T) {
	if got := tail("abcdef", 3); got != "def" {
		t.Errorf("tail = %q, want %q", got, "def")
	}
	if got := tail("ab", 10); got != "ab" {
		t.Errorf("tail = %q, want %q", got, "ab")
	}
}

func TestTailBufferKeepsOnlyTrailingBytes(t *testing.T) {
	tb := &tailBuffer{maxBytes: 5}
	tb.Write([]byte("hello"))
	tb.Write([]byte("world"))
	if got := tb.String(); got != "world" {
		t.Errorf("tailBuffer = %q, want %q", got, "world")
	}
}
