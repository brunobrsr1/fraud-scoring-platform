package scoring

import "fmt"

type FeatureErrorKind uint8

const (
	FeatureMissing FeatureErrorKind = iota + 1
	FeatureUnknown
	FeatureNull
	FeatureInvalidType
	FeatureNonFinite
)

type FeatureError struct {
	Kind    FeatureErrorKind
	Feature string
	Got     string
}

// ClientError marks a scoring failure caused by the request's input. Message
// is safe to return to the caller; Err keeps the cause for errors.Is/As.
type ClientError struct {
	Message string
	Err     error
}

func (e *FeatureError) Error() string {
	switch e.Kind {
	case FeatureMissing:
		return fmt.Sprintf(
			`scoring: feature %q is missing`,
			e.Feature,
		)
	case FeatureUnknown:
		return fmt.Sprintf(
			`scoring: feature %q is unknown`,
			e.Feature,
		)
	case FeatureNull:
		return fmt.Sprintf(
			`scoring: feature %q is null`,
			e.Feature,
		)
	case FeatureInvalidType:
		return fmt.Sprintf(
			`scoring: feature %q must be a number, got %s`,
			e.Feature,
			e.Got,
		)
	case FeatureNonFinite:
		return fmt.Sprintf(
			`scoring: feature %q must be finite`,
			e.Feature,
		)
	default:
		return fmt.Sprintf(
			`scoring: feature %q is invalid`,
			e.Feature,
		)
	}
}

func (e *ClientError) Error() string {
	return e.Message
}

func (e *ClientError) Unwrap() error {
	return e.Err
}
