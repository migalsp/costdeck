package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// writeObject writes a Kubernetes object without metadata.managedFields: server-side
// bookkeeping that often outweighs the rest of the object and that no client reads.
func writeObject(w http.ResponseWriter, status int, obj metav1.Object) {
	obj.SetManagedFields(nil)
	writeJSON(w, status, obj)
}

// writeObjects is writeObject for a list's items.
func writeObjects[T any, PT interface {
	*T
	metav1.Object
}](w http.ResponseWriter, status int, items []T) {
	for i := range items {
		PT(&items[i]).SetManagedFields(nil)
	}
	writeJSON(w, status, items)
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
