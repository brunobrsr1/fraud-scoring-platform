package httpapi

import (
	"bytes"
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
	// Maximum size of the request body in bytes
	maxRequestBody = 64 << 10 // 64 KiB
	// Maximum length of the transaction ID in bytes
	maxTransactionID = 128
)

// scoreRequest is the request HTTP format
type scoreRequest struct {
	TransactionID string           `json:"transaction_id"`
	Features      scoring.Features `json:"features"`
}

type scoreResponse struct {
	TransactionID string    `json:"transaction_id"`
	Score         float64   `json:"score"`
	Meta          scoreMeta `json:"meta"`
}

type scoreMeta struct {
	ModelVersion string `json:"model_version"`
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

		// Limit the request body before reading it so oversized requests are rejected
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

		body, err := io.ReadAll(r.Body)
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}

			writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to read request body: %v", err))
			return
		}

		if err := validateJSONStructure(body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		var req scoreRequest
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()

		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
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
		if req.Features == nil {
			writeError(w, http.StatusBadRequest, "features is required")
			return
		}

		logger.Info("scoring request", "transaction_id", req.TransactionID)

		// Call the scoring service
		result, err := service.Score(req.Features)
		if err != nil {
			var clientErr *scoring.ClientError
			if errors.As(err, &clientErr) {
				writeError(w, http.StatusBadRequest, clientErr.Error())
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
				Meta: scoreMeta{
					ModelVersion: result.ModelVersion,
				},
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
	data, err := json.Marshal(value)
	if err != nil {
		errorData, _ := json.Marshal(errorResponse{
			Error: "failed to serialize response",
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(errorData)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}

var allowedTopLevelFields = map[string]struct{}{
	"transaction_id": {},
	"features":       {},
}

// validateJSONStructure validates the JSON syntax and rejects duplicate
// object keys at every nesting level. At the top level it also requires
// exact field names.
func validateJSONStructure(data []byte) error {
	// Checked up front: Token returns a bare io.EOF both for an empty body and
	// for one cut off mid-value, so EOF alone can't tell them apart. These are
	// the four whitespace bytes JSON allows.
	if len(bytes.Trim(data, " \t\r\n")) == 0 {
		return errors.New("request body is empty")
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	if err := consumeJSONValue(dec, true); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("invalid JSON: unexpected end of input")
		}
		return fmt.Errorf("invalid JSON: %v", err)
	}

	// consumeJSONValue must consume the complete top-level JSON value.
	// Any additional token means the body contains more than one JSON value.
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain exactly one JSON object")
		}
		return fmt.Errorf("invalid trailing JSON: %v", err)
	}

	return nil
}

// consumeJSONValue consumes exactly one JSON value from dec.
func consumeJSONValue(dec *json.Decoder, topLevel bool) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}

	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			return consumeJSONObject(dec, topLevel)

		case '[':
			if topLevel {
				return errors.New("request body must contain a JSON object")
			}
			return consumeJSONArray(dec)

		default:
			return fmt.Errorf("unexpected JSON delimiter %q", value)
		}

	default:
		// Strings, numbers, booleans and null are complete JSON values.
		return nil
	}
}

// consumeJSONObject consumes one complete JSON object and rejects duplicate
// keys. Top-level objects additionally require exact field names.
func consumeJSONObject(dec *json.Decoder, topLevel bool) error {
	seen := make(map[string]struct{})

	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return err
		}

		key, ok := token.(string)
		if !ok {
			return errors.New("invalid JSON object key")
		}

		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate JSON field %q", key)
		}
		seen[key] = struct{}{}

		if topLevel {
			if _, allowed := allowedTopLevelFields[key]; !allowed {
				return fmt.Errorf("unknown top-level field %q", key)
			}
		}

		if err := consumeJSONValue(dec, false); err != nil {
			return err
		}
	}

	token, err := dec.Token()
	if err != nil {
		return err
	}

	if token != json.Delim('}') {
		return errors.New("invalid JSON object")
	}

	return nil
}

// consumeJSONArray consumes one complete JSON array.
func consumeJSONArray(dec *json.Decoder) error {
	for dec.More() {
		if err := consumeJSONValue(dec, false); err != nil {
			return err
		}
	}

	token, err := dec.Token()
	if err != nil {
		return err
	}

	if token != json.Delim(']') {
		return errors.New("invalid JSON array")
	}

	return nil
}
