package model

import (
	"errors"
	"math"
	"testing"

	"github.com/brunobrsr1/fraud-scoring-platform/internal/testfixtures"
)

// Golden vectors live in internal/testfixtures, shared with other packages.

const parityTol = 1e-9

// TestGoldenParity is the crown-jewel test: load the REAL frozen artifact and
// prove the Go dot-product + sigmoid matches Python to <1e-9.
func TestGoldenParity(t *testing.T) {
	m := loadFrozenModel(t)

	cases := []struct {
		name string
		vec  []float64
		want float64
	}{
		{"legit", testfixtures.GoldenLegit, testfixtures.GoldenLegitScore},
		{"fraud", testfixtures.GoldenFraud, testfixtures.GoldenFraudScore},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := m.Score(tc.vec)
			if err != nil {
				t.Fatalf("Score: %v", err)
			}
			if diff := math.Abs(got - tc.want); diff >= parityTol {
				t.Fatalf("parity: got %.17g, want %.17g, |Δ|=%.3e (tol %.0e)", got, tc.want, diff, parityTol)
			}
		})
	}
}

// TestFrozenArtifactShape guards the contract with training/train.py: 30
// features, aligned weights, version present.
func TestFrozenArtifactShape(t *testing.T) {
	m := loadFrozenModel(t)
	if m.Version == "" {
		t.Error("empty model_version")
	}
	if m.FeatureCount() != 30 {
		t.Errorf("FeatureCount() = %d, want 30", m.FeatureCount())
	}
	if len(m.FeatureOrder) != len(m.Weights) {
		t.Errorf("feature_order/weights length mismatch: %d vs %d", len(m.FeatureOrder), len(m.Weights))
	}
	if got := m.FeatureOrder[0]; got != "Time" {
		t.Errorf("feature_order[0] = %q, want Time", got)
	}
	if got := m.FeatureOrder[len(m.FeatureOrder)-1]; got != "Amount" {
		t.Errorf("feature_order[last] = %q, want Amount", got)
	}
}

func TestParseValid(t *testing.T) {
	data := []byte(`{
		"model_version": "v9.9.9",
		"created_at": "2026-01-01",
		"feature_order": ["A", "B"],
		"weights": [0.5, -1.5],
		"intercept": 0.25
	}`)
	m, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.Version != "v9.9.9" || m.Intercept != 0.25 || m.FeatureCount() != 2 {
		t.Errorf("unexpected parse result: %+v", m)
	}
}

func TestParseInvalid(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"malformed", `{`},
		{"empty version", `{"model_version":"","feature_order":["A"],"weights":[1],"intercept":0}`},
		{"empty feature_order", `{"model_version":"v1","feature_order":[],"weights":[],"intercept":0}`},
		{"length mismatch", `{"model_version":"v1","feature_order":["A","B","C"],"weights":[1,2],"intercept":0}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.json)); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

// TestValidateNonFinite covers the NaN/Inf guards, which JSON literals cannot
// express — so it exercises validate() directly (white-box).
func TestValidateNonFinite(t *testing.T) {
	cases := []struct {
		name string
		m    Model
	}{
		{"NaN weight", Model{Version: "v1", FeatureOrder: []string{"A"}, Weights: []float64{math.NaN()}}},
		{"Inf weight", Model{Version: "v1", FeatureOrder: []string{"A"}, Weights: []float64{math.Inf(1)}}},
		{"NaN intercept", Model{Version: "v1", FeatureOrder: []string{"A"}, Weights: []float64{1}, Intercept: math.NaN()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.m.validate(); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestScoreFeatureCountMismatch(t *testing.T) {
	m := &Model{Version: "v1", FeatureOrder: []string{"A", "B"}, Weights: []float64{1, 1}}
	if _, err := m.Score([]float64{1}); !errors.Is(err, ErrFeatureCount) {
		t.Fatalf("got err %v, want ErrFeatureCount", err)
	}
}

func TestScoreNonFiniteFeature(t *testing.T) {
	m := &Model{Version: "v1", FeatureOrder: []string{"A"}, Weights: []float64{1}}
	if _, err := m.Score([]float64{math.NaN()}); err == nil {
		t.Fatal("expected error for NaN feature, got nil")
	}
}

// TestScoreOverflowLogit covers finite-but-extreme inputs whose dot-product
// overflows to a non-finite logit (+Inf + -Inf => NaN). Score must error, not
// return NaN, so the [0,1] probability invariant is never violated.
func TestScoreOverflowLogit(t *testing.T) {
	m := &Model{Version: "v1", FeatureOrder: []string{"A", "B"}, Weights: []float64{1e300, 1e300}}
	if _, err := m.Score([]float64{1e300, -1e300}); err == nil {
		t.Fatal("expected error for overflowing logit, got nil")
	}
}

// TestScoreInRange checks the probability invariant across a wide range of
// logits, including magnitudes that would overflow a naive sigmoid.
func TestScoreInRange(t *testing.T) {
	m := &Model{Version: "v1", FeatureOrder: []string{"A"}, Weights: []float64{1}}
	for _, x := range []float64{-1e6, -100, -1, 0, 1, 100, 1e6} {
		got, err := m.Score([]float64{x})
		if err != nil {
			t.Fatalf("Score(%v): %v", x, err)
		}
		if got < 0 || got > 1 || math.IsNaN(got) {
			t.Errorf("Score(%v) = %v, want a probability in [0,1]", x, got)
		}
	}
}

func TestSigmoid(t *testing.T) {
	if got := sigmoid(0); got != 0.5 {
		t.Errorf("sigmoid(0) = %v, want 0.5", got)
	}
	// Large magnitudes must saturate without overflow/NaN.
	if got := sigmoid(1000); got != 1 {
		t.Errorf("sigmoid(1000) = %v, want 1", got)
	}
	if got := sigmoid(-1000); got != 0 {
		t.Errorf("sigmoid(-1000) = %v, want 0", got)
	}
	// Symmetry: sigmoid(-z) == 1 - sigmoid(z).
	if got, want := sigmoid(-2), 1-sigmoid(2); math.Abs(got-want) > 1e-15 {
		t.Errorf("sigmoid symmetry broken: %v vs %v", got, want)
	}
}

// --- helpers --------------------------------------------------------------

func loadFrozenModel(t *testing.T) *Model {
	t.Helper()
	path := testfixtures.FrozenModelPath(t)
	m, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	return m
}
