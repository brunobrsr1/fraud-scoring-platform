// Package model is the pure inference kernel of the fraud-scoring service.
//
// It loads the frozen model artifact (models/vX.Y.Z/model.json), validates it,
// and computes score = sigmoid(features · weights + intercept) over RAW,
// unscaled features. The StandardScaler was folded algebraically into the
// exported weights offline (see training/train.py), so there is no scaling step
// and no ML runtime here: just a dot-product and a sigmoid.
//
// The kernel knows nothing about HTTP, the request payload, the registry, or
// Raft. It receives the feature vector already in feature_order and returns a
// probability in [0,1] — never a verdict. Mapping a request payload to a feature
// vector is an upstream concern (OQ-6) and deliberately lives elsewhere.
package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
)

// ErrFeatureCount is returned by Score when the input vector length does not
// match the model's weight count. Callers can match it with errors.Is.
var ErrFeatureCount = errors.New("model: feature count mismatch")

// Model is the parsed, validated model artifact. It maps 1:1 onto model.json.
// Weights is positional and aligned index-for-index with FeatureOrder; that
// alignment is the whole contract with the offline training pipeline.
type Model struct {
	Version      string    `json:"model_version"`
	CreatedAt    string    `json:"created_at"`
	FeatureOrder []string  `json:"feature_order"`
	Weights      []float64 `json:"weights"`
	Intercept    float64   `json:"intercept"`
}

// Parse unmarshals and validates a model artifact from its JSON bytes. The
// source of the bytes (embedded, file, registry) is the caller's choice, which
// keeps this kernel pure and easy to test.
func Parse(data []byte) (*Model, error) {
	var m Model
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("model: parse artifact: %w", err)
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Load reads and parses a model artifact from a file. It is a convenience over
// Parse for the v0 local single-node path.
func Load(path string) (*Model, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("model: read artifact %q: %w", path, err)
	}
	return Parse(data)
}

// validate rejects a structurally invalid artifact at load time, so Score can
// stay free of defensive checks on immutable state.
func (m *Model) validate() error {
	if m.Version == "" {
		return errors.New("model: empty model_version")
	}
	if len(m.FeatureOrder) == 0 {
		return errors.New("model: empty feature_order")
	}
	if len(m.Weights) != len(m.FeatureOrder) {
		return fmt.Errorf("model: weights/feature_order length mismatch: %d weights, %d features",
			len(m.Weights), len(m.FeatureOrder))
	}
	for i, w := range m.Weights {
		if !isFinite(w) {
			return fmt.Errorf("model: non-finite weight at index %d (%s): %v", i, m.FeatureOrder[i], w)
		}
	}
	if !isFinite(m.Intercept) {
		return fmt.Errorf("model: non-finite intercept: %v", m.Intercept)
	}
	return nil
}

// Score computes the fraud probability for a feature vector. The vector must be
// in feature_order and have exactly FeatureCount() entries. The result is a
// probability in [0,1] (guaranteed by sigmoid), never a verdict; thresholding is
// the caller's business logic.
func (m *Model) Score(features []float64) (float64, error) {
	if len(features) != len(m.Weights) {
		return 0, fmt.Errorf("%w: got %d, want %d", ErrFeatureCount, len(features), len(m.Weights))
	}
	// Reject NaN/Inf inputs rather than silently returning a NaN/degenerate score.
	z := m.Intercept
	for i, x := range features {
		if !isFinite(x) {
			return 0, fmt.Errorf("model: non-finite feature at index %d: %v", i, x)
		}
		z += x * m.Weights[i]
	}
	// Even with finite inputs, extreme magnitudes can overflow the dot-product to
	// ±Inf (or NaN via +Inf + -Inf). Guard the accumulated logit so Score never
	// returns a non-finite "probability" and the [0,1] invariant holds strictly.
	if !isFinite(z) {
		return 0, fmt.Errorf("model: non-finite logit from feature magnitudes: %v", z)
	}
	// Sequential summation is well within the <1e-9 parity budget vs numpy's
	// pairwise sum (the difference lands around 1e-15).
	return sigmoid(z), nil
}

// FeatureCount is the number of features (and weights) the model expects. A
// caller builds its input vector in m.FeatureOrder and must not mutate that
// slice — the model is treated as immutable after Load.
func (m *Model) FeatureCount() int { return len(m.Weights) }

// sigmoid is the numerically stable logistic function, mirroring
// scipy.special.expit. Branching on the sign of z avoids overflow of exp for
// large |z|, so the output stays in (0,1) across the whole real line.
func sigmoid(z float64) float64 {
	if z >= 0 {
		return 1 / (1 + math.Exp(-z))
	}
	ez := math.Exp(z)
	return ez / (1 + ez)
}

func isFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}
