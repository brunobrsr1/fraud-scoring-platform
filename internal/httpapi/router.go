package httpapi

import "net/http"

func New() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", healthHandler)

	return mux
}
