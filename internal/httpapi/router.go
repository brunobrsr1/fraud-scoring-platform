// Package httpapi holds the HTTP routes and handlers.
package httpapi

import "net/http"

// New returns a Handler, not a Server: main owns the lifecycle and timeouts.
func New() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", healthHandler)

	return mux
}
