package api

import (
	"encoding/json"
	"net/http"
)

/* errorResponse is the canonical error envelope returned by every endpoint. */
type errorResponse struct {
	Error string `json:"error"`
}

/*
writeError serialises a consistent {"error": "..."} JSON body with the given
HTTP status code. Every handler uses this — no raw http.Error calls.
*/
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(errorResponse{Error: msg}) //nolint:errcheck
}

/*
writeJSON serialises v to JSON, sets Content-Type and the given status code.
Used for all successful responses.
*/
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
