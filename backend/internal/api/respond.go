// Package api exposes the REST surface the SvelteKit frontend talks to.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// errorBody is the single error shape every failing endpoint returns, so the
// frontend has exactly one thing to parse.
type errorBody struct {
	Error string `json:"error"`
}

// writeJSON serialises v with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already sent, so this can only be logged.
		slog.Error("write json response", "error", err)
	}
}

// writeError returns a JSON error. msg is shown to the user, so it must not
// leak internal detail — log the underlying error separately.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

// decodeJSON reads a JSON request body into v, rejecting unknown fields so a
// typo in the frontend fails loudly instead of silently doing nothing.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
