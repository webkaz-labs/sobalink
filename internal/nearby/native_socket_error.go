package nearby

import "errors"

// Fixed stages distinguish adapter failures without exposing addresses,
// interface identifiers, paths or underlying platform error text.
type nativeSocketStage string

type nativeSocketError struct{ stage nativeSocketStage }

func (e nativeSocketError) Error() string         { return ErrSocketUnavailable.Error() }
func (e nativeSocketError) Unwrap() error         { return ErrSocketUnavailable }
func socketFailure(stage nativeSocketStage) error { return nativeSocketError{stage: stage} }
func socketFailureStage(err error) string {
	var failure nativeSocketError
	if errors.As(err, &failure) {
		return string(failure.stage)
	}
	return "none"
}
