// Package httpapi holds the HTTP routes and handlers.
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/brunobrsr1/fraud-scoring-platform/internal/scoring"
)

// New returns a Handler, not a Server: main owns the lifecycle and timeouts.
func New(service *scoring.Service, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", healthHandler)

	mux.HandleFunc("GET /readyz", readyHandler(service))

	mux.HandleFunc("POST /score", scoreHandler(service, logger))

	return mux
}
