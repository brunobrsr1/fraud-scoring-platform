package httpapi

import (
	"log/slog"
	"net/http"
)

type readiness interface {
	Ready() bool
}

func readyHandler(service readiness) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if service.Ready() {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
	}
}

var _ slog.Handler
