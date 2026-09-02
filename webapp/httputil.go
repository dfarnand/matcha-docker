package main

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Nothing behind this app should ever be cached by an intermediary; the
	// JSON API carries settings and run state.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json response: %v", err)
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": msg})
}

// decodeJSONBody reads a bounded JSON request body.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(dst)
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

func parseRequestURL(s string) (*url.URL, error) { return url.Parse(s) }
