package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// maxRequestBody caps JSON request bodies. The largest legitimate payload is a saved AI
// report, which stays well below this.
const maxRequestBody = 4 << 20

// writeJSON encodes v as the JSON response body with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logf.Log.Error(err, "Could not encode JSON response")
	}
}

// writeError writes a JSON error body: {"error": "<message>"}.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeErrorf is writeError with fmt.Sprintf formatting.
func writeErrorf(w http.ResponseWriter, status int, format string, args ...any) {
	writeError(w, status, fmt.Sprintf(format, args...))
}

// decodeJSON decodes a size-capped JSON request body into v. An empty body leaves v
// untouched, which lets endpoints treat "no body" and "{}" the same way.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(v)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
