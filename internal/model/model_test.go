package model

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// --- Golden vectors -------------------------------------------------------
//
// Captured by training/golden.py, which scores these two dataset rows with the
// frozen artifact's own weights. A green TestGoldenParity proves the Go kernel
// agrees with Python (scipy) to <1e-9 on the exact model.json we ship.
//
// To regenerate after a (rare) model change:
//   cd training && source .venv/bin/activate.fish && python golden.py

// goldenLegit is dataset row 0 (Class=0), in feature_order.
var goldenLegit = []float64{
	0.0, -1.3598071336738, -0.0727811733098497, 2.53634673796914, 1.37815522427443,
	-0.338320769942518, 0.462387777762292, 0.239598554061257, 0.0986979012610507,
	0.363786969611213, 0.0907941719789316, -0.551599533260813, -0.617800855762348,
	-0.991389847235408, -0.311169353699879, 1.46817697209427, -0.470400525259478,
	0.207971241929242, 0.0257905801985591, 0.403992960255733, 0.251412098239705,
	-0.018306777944153, 0.277837575558899, -0.110473910188767, 0.0669280749146731,
	0.128539358273528, -0.189114843888824, 0.133558376740387, -0.0210530534538215,
	149.62,
}

const goldenLegitScore = 0.26201699054165745

// goldenFraud is dataset row 541 (Class=1), in feature_order.
var goldenFraud = []float64{
	406.0, -2.3122265423263, 1.95199201064158, -1.60985073229769, 3.9979055875468,
	-0.522187864667764, -1.42654531920595, -2.53738730624579, 1.39165724829804,
	-2.77008927719433, -2.77227214465915, 3.20203320709635, -2.89990738849473,
	-0.595221881324605, -4.28925378244217, 0.389724120274487, -1.14074717980657,
	-2.83005567450437, -0.0168224681808257, 0.416955705037907, 0.126910559061474,
	0.517232370861764, -0.0350493686052974, -0.465211076182388, 0.320198198514526,
	0.0445191674731724, 0.177839798284401, 0.261145002567677, -0.143275874698919,
	0.0,
}

const goldenFraudScore = 0.99999987775230792

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
		{"legit", goldenLegit, goldenLegitScore},
		{"fraud", goldenFraud, goldenFraudScore},
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
	path := filepath.Join(repoRoot(t), "models", "v1.0.0", "model.json")
	m, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	return m
}

// repoRoot walks up from this test file until it finds go.mod, so the golden
// test stays bound to the committed artifact rather than a drift-prone copy.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from test file")
		}
		dir = parent
	}
}
