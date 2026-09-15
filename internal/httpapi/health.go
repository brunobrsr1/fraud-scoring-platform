package httpapi

import (
	"encoding/json"
	"net/http"
)

// Liveness only. It must not check the model or the registry, or a dependency
// outage gets the whole fleet restarted.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	// The status is already sent, so there's nothing to do with an error.
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
	})
}
