package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func pathSegment(r *http.Request, pos int) string {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if pos >= len(parts) {
		return ""
	}
	return parts[pos]
}

func userFromRequest(r *http.Request) (id, name string) {
	id = r.Header.Get("X-User-ID")
	name = r.Header.Get("X-User-Name")
	if id == "" {
		id = "web-user"
	}
	if name == "" {
		name = "Web User"
	}
	return
}
