package scoring

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/brunobrsr1/fraud-scoring-platform/internal/model"
)

type Features map[string]any

type Result struct {
	Score        float64
	ModelVersion string
	ScoredAt     time.Time
}

type Service struct {
	model *model.Model
	now   func() time.Time
}

func New(m *model.Model, now func() time.Time) (*Service, error) {
	if m == nil {
		return nil, fmt.Errorf("scoring: nil model")
	}
	if now == nil {
		now = time.Now
	}

	return &Service{
		model: m,
		now:   now,
	}, nil
}

func (s *Service) activeModel() *model.Model {
	return s.model
}

func vectorFor(m *model.Model, features Features) ([]float64, error) {
	expected := make(map[string]struct{}, len(m.FeatureOrder))
	for _, name := range m.FeatureOrder {
		expected[name] = struct{}{}
	}

	vector := make([]float64, len(m.FeatureOrder))

	for i, name := range m.FeatureOrder {
		raw, ok := features[name]
		if !ok {
			return nil, &FeatureError{
				Kind:    FeatureMissing,
				Feature: name,
			}
		}
		if raw == nil {
			return nil, &FeatureError{
				Kind:    FeatureNull,
				Feature: name,
			}
		}

		x, ok := raw.(float64)
		if !ok {
			return nil, &FeatureError{
				Kind:    FeatureInvalidType,
				Feature: name,
				Got:     fmt.Sprintf("%T", raw),
			}
		}

		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, &FeatureError{
				Kind:    FeatureNonFinite,
				Feature: name,
			}
		}
		vector[i] = x
	}
	var unknown []string
	for name := range features {
		if _, ok := expected[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, &FeatureError{
			Kind:    FeatureUnknown,
			Feature: unknown[0],
		}
	}

	return vector, nil
}

func (s *Service) Score(features Features) (Result, error) {
	m := s.activeModel()

	vector, err := vectorFor(m, features)
	if err != nil {
		return Result{}, &ClientError{Message: err.Error(), Err: err}
	}

	score, err := m.Score(vector)
	if err != nil {
		// Every feature is finite, but together they overflow the model. The
		// cause stays in Err; the caller gets a message it can act on.
		if errors.Is(err, model.ErrNonFiniteLogit) {
			return Result{}, &ClientError{
				Message: "scoring: feature values are too large to score",
				Err:     err,
			}
		}
		return Result{}, err
	}

	return Result{
		Score:        score,
		ModelVersion: m.Version,
		ScoredAt:     s.now(),
	}, nil
}
