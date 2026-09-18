package scoring

import (
	"fmt"
	"testing"
	"time"

	"github.com/brunobrsr1/fraud-scoring-platform/internal/model"
	"github.com/brunobrsr1/fraud-scoring-platform/internal/testfixtures"
)

func frozenModel(t *testing.T) *model.Model {
	t.Helper()
	m, err := model.Load(testfixtures.FrozenModelPath(t))
	if err != nil {
		t.Fatalf("load frozen model: %v", err)
	}
	return m
}

// featureMap builds a request-shaped map from a vector in feature_order.
func featureMap(order []string, values []float64) Features {
	if len(order) != len(values) {
		panic(fmt.Sprintf("featureMap: %d names, %d values", len(order), len(values)))
	}
	f := make(Features, len(order))
	for i, name := range order {
		f[name] = values[i]
	}
	return f
}

// cloneFeatures gives each subtest its own map: maps are references, so a
// mutation in one case would otherwise leak into the next.
func cloneFeatures(f Features) Features {
	c := make(Features, len(f))
	for k, v := range f {
		c[k] = v
	}
	return c
}

func fixedNow() time.Time {
	return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
}
