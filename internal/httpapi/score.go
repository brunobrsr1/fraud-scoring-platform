package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"

	"github.com/brunobrsr1/fraud-scoring-platform/internal/scoring"
)

const (
	// Maximum size of the request body in bytes.
	maxRequestBody = 64 << 10 // 64 KiB
	// Maximum length of the transaction ID in characters.
	maxTransactionID = 128
)

// scoreRequest is the request HTTP format
type scoreRequest struct {
	TransactionID string           `json:"transaction_id"`
	Features      scoring.Features `json:"features"`
}

type scoreResponse struct {
	TransactionID string  `json:"transaction_id"`
	Score         float64 `json:"score"`
	ModelVersion  string  `json:"model_version"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// transforms the HTTP contract into domain contract
func scoreHandler(service *scoring.Service, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only allow application/json content type
		if err := requireJSON(r); err != nil {
			writeError(w, http.StatusUnsupportedMediaType, err.Error())
			return
		}

		// the decoder can't consume more than maxRequestBody bytes
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

		var req scoreRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()

		if err := dec.Decode(&req); err != nil {
			// Handle specific errors
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			if errors.Is(err, io.EOF) {
				writeError(w, http.StatusBadRequest, "request body is empty")
				return
			}
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
			return
		}

		// Exactly one JSON value is allowed. A second value, including another
		// object after the first one, is rejected.
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			// If err is nil, it means there was a second JSON value, which is not allowed.
			if err == nil {
				writeError(w, http.StatusBadRequest, "request body must contain exactly one JSON object")
				return
			}
			var maxErr *http.MaxBytesError
			// Handle specific errors
			if errors.As(err, &maxErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid trailing JSON: %v", err))
			return
		}

		// Validate transaction_id
		if req.TransactionID == "" {
			writeError(w, http.StatusBadRequest, "transaction_id is required")
			return
		}
		if len(req.TransactionID) > maxTransactionID {
			writeError(w, http.StatusBadRequest, "transaction_id is too long")
			return
		}

		logger.Info("scoring request", "transaction_id", req.TransactionID)

		// Call the scoring service
		result, err := service.Score(req.Features)
		if err != nil {
			var featureErr *scoring.FeatureError
			if errors.As(err, &featureErr) {
				writeError(w, http.StatusBadRequest, featureErr.Error())
				return
			}
			logger.Error("scoring failed", "transaction_id", req.TransactionID, "err", err)
			writeError(w, http.StatusInternalServerError, "scoring failed")
			return
		}

		// Serialized response
		writeJSON(
			w,
			http.StatusOK,
			scoreResponse{
				TransactionID: req.TransactionID,
				Score:         result.Score,
				ModelVersion:  result.ModelVersion,
			})
	}
}

// guarantees that the request has a Content-Type of application/json
func requireJSON(r *http.Request) error {
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		return errors.New("Content-Type must be application/json")
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}
