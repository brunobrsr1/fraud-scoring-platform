package httpapi

import "net/http"

type readiness interface {
	Ready() bool
}

func readyHandler(service readiness) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if service.Ready() {
			writeJSON(
				w,
				http.StatusOK,
				map[string]string{
					"status": "ready",
				},
			)
			return
		}
		// if the model becomes unavailable, /readyz should return 503
		writeJSON(
			w,
			http.StatusServiceUnavailable,
			map[string]string{
				"status": "not_ready",
			},
		)
	}
}
