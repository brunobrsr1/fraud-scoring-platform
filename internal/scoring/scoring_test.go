package scoring

import (
	"errors"
	"math"
	"testing"

	"github.com/brunobrsr1/fraud-scoring-platform/internal/testfixtures"
)

func TestScoreInvalidFeatures(t *testing.T) {
	m := frozenModel(t)
	base := featureMap(m.FeatureOrder, testfixtures.GoldenLegit)

	cases := []struct {
		name        string
		mutate      func(Features)
		wantKind    FeatureErrorKind
		wantFeature string
	}{
		{
			name: "missing",
			mutate: func(f Features) {
				delete(f, "V17")
			},
			wantKind:    FeatureMissing,
			wantFeature: "V17",
		},
		{
			name: "unknown",
			mutate: func(f Features) {
				f["V29"] = 0.0
			},
			wantKind:    FeatureUnknown,
			wantFeature: "V29",
		},
		{
			name: "null",
			mutate: func(f Features) {
				f["V17"] = nil
			},
			wantKind:    FeatureNull,
			wantFeature: "V17",
		},
		{
			name: "string",
			mutate: func(f Features) {
				f["V17"] = "abc"
			},
			wantKind:    FeatureInvalidType,
			wantFeature: "V17",
		},
		{
			name: "nan",
			mutate: func(f Features) {
				f["V17"] = math.NaN()
			},
			wantKind:    FeatureNonFinite,
			wantFeature: "V17",
		},
		{
			name: "positive infinity",
			mutate: func(f Features) {
				f["V17"] = math.Inf(1)
			},
			wantKind:    FeatureNonFinite,
			wantFeature: "V17",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			features := cloneFeatures(base)
			tc.mutate(features)

			service, err := New(m, fixedNow)
			if err != nil {
				t.Fatal(err)
			}

			_, err = service.Score(features)
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			var featureErr *FeatureError
			if !errors.As(err, &featureErr) {
				t.Fatalf(
					"error type = %T, want *FeatureError: %v",
					err,
					err,
				)
			}

			if featureErr.Kind != tc.wantKind {
				t.Errorf(
					"error kind = %v, want %v",
					featureErr.Kind,
					tc.wantKind,
				)
			}

			if featureErr.Feature != tc.wantFeature {
				t.Errorf(
					"error feature = %q, want %q",
					featureErr.Feature,
					tc.wantFeature,
				)
			}
		})
	}
}

func TestScoreGoldenLegit(t *testing.T) {
	m := frozenModel(t)

	want, err := m.Score(testfixtures.GoldenLegit)
	if err != nil {
		t.Fatalf("model.Score: %v", err)
	}

	service, err := New(m, fixedNow)
	if err != nil {
		t.Fatal(err)
	}

	got, err := service.Score(featureMap(m.FeatureOrder, testfixtures.GoldenLegit))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}

	// scoring must preserve the exact model output.
	if got.Score != want {
		t.Fatalf(
			"scoring changed model output: got %.17g, want %.17g",
			got.Score,
			want,
		)
	}

	if got.ModelVersion != "v1.0.0" {
		t.Fatalf("model version = %q, want %q", got.ModelVersion, "v1.0.0")
	}

	if !got.ScoredAt.Equal(fixedNow()) {
		t.Fatalf("scored_at = %v, want %v", got.ScoredAt, fixedNow())
	}
}
