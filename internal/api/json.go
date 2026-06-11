package api

import (
	"encoding/json"
	"net/http"
)

// respondJSON writes a JSON-encoded body with the given status code.
// Content-Type is set to application/json.
func respondJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// errBody returns a standard error response map.
func errBody(msg string) map[string]string {
	return map[string]string{"error": msg}
}

// requireJSON enforces Content-Type: application/json for POST/PUT/PATCH.
// Returns false (and writes a 415) when the check fails.
func requireJSON(w http.ResponseWriter, r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if ct != "application/json" && ct != "application/json; charset=utf-8" {
		respondJSON(w, http.StatusUnsupportedMediaType,
			errBody("Content-Type must be application/json"))
		return false
	}
	return true
}
