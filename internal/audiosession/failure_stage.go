package audiosession

import "errors"

// Keep the original error available for recovery while identifying the boundary
// that produced it. The stage is safe to persist without vendor error payloads.
type failureAt struct {
	stage string
	cause error
}

func (e *failureAt) Error() string { return e.cause.Error() }
func (e *failureAt) Unwrap() error { return e.cause }

func atStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &failureAt{stage: stage, cause: err}
}

func FailureStage(err error) string {
	var e *failureAt
	if errors.As(err, &e) {
		return e.stage
	}
	return ""
}
