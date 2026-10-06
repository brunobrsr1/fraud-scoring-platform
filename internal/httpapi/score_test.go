package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brunobrsr1/fraud-scoring-platform/internal/model"
	"github.com/brunobrsr1/fraud-scoring-platform/internal/scoring"
	"github.com/brunobrsr1/fraud-scoring-platform/internal/testfixtures"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()

	m, err := model.Load(testfixtures.FrozenModelPath(t))
	if err != nil {
		t.Fatalf("load model: %v", err)
	}

	service, err := scoring.New(m, nil)
	if err != nil {
		t.Fatalf("create scoring service: %v", err)
	}

	logger := slog.New(
		slog.NewTextHandler(io.Discard, nil),
	)

	return New(service, logger)
}

// scoreContract is the documented response shape, written out independently of
// scoreResponse: if the handler's struct drifts, this test should notice.
type scoreContract struct {
	TransactionID string  `json:"transaction_id"`
	Score         float64 `json:"score"`
	Meta          struct {
		ModelVersion string `json:"model_version"`
	} `json:"meta"`
}

// featureOrder reads the order from the frozen artifact rather than repeating
// it here.
func featureOrder(t *testing.T) []string {
	t.Helper()

	m, err := model.Load(testfixtures.FrozenModelPath(t))
	if err != nil {
		t.Fatalf("load model: %v", err)
	}
	return m.FeatureOrder
}

// requestBody builds a valid request from a vector in feature_order. mutate,
// if non-nil, edits the features before they are encoded.
func requestBody(
	t *testing.T,
	transactionID string,
	values []float64,
	mutate func(map[string]any),
) []byte {
	t.Helper()

	order := featureOrder(t)
	features := make(map[string]any, len(order))
	for i, name := range order {
		features[name] = values[i]
	}
	if mutate != nil {
		mutate(features)
	}

	body, err := json.Marshal(map[string]any{
		"transaction_id": transactionID,
		"features":       features,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return body
}

func doRequest(
	t *testing.T,
	h http.Handler,
	method, path, contentType string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestScoreGolden(t *testing.T) {
	h := testHandler(t)

	cases := []struct {
		name   string
		values []float64
		want   float64
	}{
		{name: "legit", values: testfixtures.GoldenLegit, want: testfixtures.GoldenLegitScore},
		{name: "fraud", values: testfixtures.GoldenFraud, want: testfixtures.GoldenFraudScore},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := requestBody(t, "tx_"+tc.name, tc.values, nil)
			rec := doRequest(t, h, http.MethodPost, "/v1/score", "application/json", body)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			// DisallowUnknownFields pins the shape: a new response field must be
			// a deliberate contract change, made here too.
			var got scoreContract
			dec := json.NewDecoder(rec.Body)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&got); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			if got.TransactionID != "tx_"+tc.name {
				t.Errorf("transaction_id = %q, want %q", got.TransactionID, "tx_"+tc.name)
			}
			if math.Abs(got.Score-tc.want) > 1e-9 {
				t.Errorf("score = %.17g, want %.17g (±1e-9)", got.Score, tc.want)
			}
			if got.Meta.ModelVersion != "v1.0.0" {
				t.Errorf("meta.model_version = %q, want %q", got.Meta.ModelVersion, "v1.0.0")
			}
		})
	}
}

func TestScoreAccepts(t *testing.T) {
	h := testHandler(t)

	cases := []struct {
		name        string
		contentType string
		body        []byte
	}{
		{
			name:        "content type with charset",
			contentType: "application/json; charset=utf-8",
			body:        requestBody(t, "tx_charset", testfixtures.GoldenLegit, nil),
		},
		{
			name:        "transaction_id at the length limit",
			contentType: "application/json",
			body: requestBody(
				t,
				strings.Repeat("a", maxTransactionID),
				testfixtures.GoldenLegit,
				nil,
			),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(t, h, http.MethodPost, "/v1/score", tc.contentType, tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestScoreRejects(t *testing.T) {
	h := testHandler(t)
	legit := testfixtures.GoldenLegit

	// Every feature finite, yet their weighted sum overflows the logit.
	overflow := make([]float64, len(legit))
	for i := range overflow {
		overflow[i] = 1e308
	}

	cases := []struct {
		name        string
		contentType string
		body        []byte
		wantStatus  int
		// wantErr is a substring of the error message. It names the problem,
		// not the exact wording, so rephrasing a message doesn't break the test.
		wantErr string
	}{
		// Content type.
		{
			name:       "no content type",
			body:       requestBody(t, "tx", legit, nil),
			wantStatus: http.StatusUnsupportedMediaType,
			wantErr:    "Content-Type",
		},
		{
			name:        "text/plain",
			contentType: "text/plain",
			body:        requestBody(t, "tx", legit, nil),
			wantStatus:  http.StatusUnsupportedMediaType,
			wantErr:     "Content-Type",
		},

		// Body framing and syntax.
		{
			name:        "empty body",
			contentType: "application/json",
			body:        nil,
			wantStatus:  http.StatusBadRequest,
			wantErr:     "empty",
		},
		{
			name:        "whitespace-only body",
			contentType: "application/json",
			body:        []byte(" \t\r\n"),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "empty",
		},
		{
			// Token reports truncation as a bare io.EOF; it must not be
			// mistaken for an empty body.
			name:        "truncated body",
			contentType: "application/json",
			body:        []byte(`{"transaction_id":`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "unexpected end",
		},
		{
			name:        "unclosed object",
			contentType: "application/json",
			body:        []byte(`{"transaction_id":"tx"`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "unexpected end",
		},
		{
			name:        "malformed JSON",
			contentType: "application/json",
			body:        []byte(`{"transaction_id" "tx"}`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "invalid JSON",
		},
		{
			name:        "two JSON values",
			contentType: "application/json",
			body:        []byte(`{"transaction_id":"tx","features":{}} {}`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "exactly one",
		},
		{
			name:        "trailing garbage",
			contentType: "application/json",
			body:        []byte(`{"transaction_id":"tx","features":{}} x`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "trailing",
		},
		{
			name:        "top-level array",
			contentType: "application/json",
			body:        []byte(`[]`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "object",
		},
		{
			name:        "body over the size limit",
			contentType: "application/json",
			// Valid JSON padded with whitespace: rejected for size alone.
			body: append(
				bytes.Repeat([]byte(" "), maxRequestBody),
				requestBody(t, "tx", legit, nil)...,
			),
			wantStatus: http.StatusRequestEntityTooLarge,
			wantErr:    "too large",
		},

		// Top-level fields: exact names, each exactly once.
		{
			name:        "unknown top-level field",
			contentType: "application/json",
			body:        []byte(`{"transaction_id":"tx","features":{},"extra":1}`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "extra",
		},
		{
			name:        "top-level field in the wrong case",
			contentType: "application/json",
			body:        []byte(`{"Transaction_ID":"tx","features":{}}`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "Transaction_ID",
		},
		{
			name:        "duplicate top-level field",
			contentType: "application/json",
			body:        []byte(`{"transaction_id":"a","transaction_id":"b","features":{}}`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "duplicate",
		},
		{
			name:        "duplicate hidden by a unicode escape",
			contentType: "application/json",
			body:        []byte(`{"transaction_id":"a","transaction_id":"b","features":{}}`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "duplicate",
		},
		{
			name:        "duplicate feature",
			contentType: "application/json",
			body:        []byte(`{"transaction_id":"tx","features":{"V1":1,"V1":2}}`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "duplicate",
		},

		// transaction_id.
		{
			name:        "transaction_id missing",
			contentType: "application/json",
			body:        []byte(`{"features":{}}`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "transaction_id is required",
		},
		{
			name:        "transaction_id not a string",
			contentType: "application/json",
			body:        []byte(`{"transaction_id":1,"features":{}}`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "transaction_id",
		},
		{
			name:        "transaction_id over the length limit",
			contentType: "application/json",
			body: requestBody(
				t,
				strings.Repeat("a", maxTransactionID+1),
				legit,
				nil,
			),
			wantStatus: http.StatusBadRequest,
			wantErr:    "too long",
		},

		// features object.
		{
			name:        "features missing",
			contentType: "application/json",
			body:        []byte(`{"transaction_id":"tx"}`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "features is required",
		},
		{
			name:        "features null",
			contentType: "application/json",
			body:        []byte(`{"transaction_id":"tx","features":null}`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "features is required",
		},
		{
			name:        "features is an array",
			contentType: "application/json",
			body:        []byte(`{"transaction_id":"tx","features":[]}`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "features",
		},

		// Individual features.
		{
			name:        "feature missing",
			contentType: "application/json",
			body: requestBody(t, "tx", legit, func(f map[string]any) {
				delete(f, "V17")
			}),
			wantStatus: http.StatusBadRequest,
			wantErr:    `"V17" is missing`,
		},
		{
			name:        "feature unknown",
			contentType: "application/json",
			body: requestBody(t, "tx", legit, func(f map[string]any) {
				f["V29"] = 0.0
			}),
			wantStatus: http.StatusBadRequest,
			wantErr:    `"V29" is unknown`,
		},
		{
			name:        "feature null",
			contentType: "application/json",
			body: requestBody(t, "tx", legit, func(f map[string]any) {
				f["V17"] = nil
			}),
			wantStatus: http.StatusBadRequest,
			wantErr:    `"V17" is null`,
		},
		{
			name:        "feature is a string",
			contentType: "application/json",
			body: requestBody(t, "tx", legit, func(f map[string]any) {
				f["V17"] = "1.0"
			}),
			wantStatus: http.StatusBadRequest,
			wantErr:    `"V17" must be a number`,
		},
		{
			// JSON has no NaN or Inf; the nearest attack is a number too big
			// for float64.
			name:        "feature out of float64 range",
			contentType: "application/json",
			body:        []byte(`{"transaction_id":"tx","features":{"Time":1e400}}`),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "1e400",
		},
		{
			name:        "finite features that overflow the logit",
			contentType: "application/json",
			body:        requestBody(t, "tx", overflow, nil),
			wantStatus:  http.StatusBadRequest,
			wantErr:     "too large to score",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(t, h, http.MethodPost, "/v1/score", tc.contentType, tc.body)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.wantStatus, rec.Body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			var got errorResponse
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if !strings.Contains(got.Error, tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", got.Error, tc.wantErr)
			}
		})
	}
}

// The handler can't produce an unmarshalable value today (the model keeps the
// score finite), so the fallback is tested on writeJSON directly.
func TestWriteJSONMarshalFailure(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, math.NaN())

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got errorResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if got.Error == "" {
		t.Error("error message is empty")
	}
}
