package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestProbes(t *testing.T) {
	h := testHandler(t)

	cases := []struct {
		path       string
		wantStatus string
	}{
		{path: "/healthz", wantStatus: "ok"},
		{path: "/readyz", wantStatus: "ready"},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			rec := doRequest(t, h, http.MethodGet, tc.path, "", nil)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}

			var got map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if got["status"] != tc.wantStatus {
				t.Errorf("status field = %q, want %q", got["status"], tc.wantStatus)
			}
		})
	}
}

// Only status codes here: 404 and 405 come from ServeMux as text/plain, not
// from our JSON error writer.
func TestRoutes(t *testing.T) {
	h := testHandler(t)

	cases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{
			name:       "wrong method on score",
			method:     http.MethodGet,
			path:       "/v1/score",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "unversioned score path is gone",
			method:     http.MethodPost,
			path:       "/score",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(t, h, tc.method, tc.path, "application/json", nil)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}
