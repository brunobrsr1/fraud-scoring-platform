package httpapi

import (
	"encoding/json"
	"net/http"
)

func healthHandler(w http.ResponseWriter, r *http.Request) {
	// Set the response header to indicate that the content type is JSON
	w.Header().Set("Content-Type", "application/json")
	// Send an HTTP 200 OK status code
	w.WriteHeader(http.StatusOK)

	// Create a simple map, encode it as JSON, and write it directly to the response
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
	})
}
