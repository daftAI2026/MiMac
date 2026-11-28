package wetype

import (
	"context"
	"time"

	"mimac.local/mimac/internal/adapters/windowlease"
)

const (
	visibilityObserveLimit = 800 * time.Millisecond
	visibilityPollInterval = 20 * time.Millisecond
)

// RecordingBarVisibilityBaseline is the opt-in WeType app-visibility path.
// It is separate from the existing AXPosition lease so default behavior is
// unchanged until the host explicitly selects this adapter.
type RecordingBarVisibilityBaseline struct{ inner *windowlease.HiddenBaseline }

type RecordingBarVisibilitySuppression struct{ *windowlease.HiddenLease }

// CaptureRecordingBarVisibilityBaseline binds the opt-in lease to the exact
// running WeType process and requires an initially visible, empty UI baseline.
func CaptureRecordingBarVisibilityBaseline(pid int32, expectedExecutable string) *RecordingBarVisibilityBaseline {
	return captureRecordingBarVisibilityBaselineNative(pid, expectedExecutable)
}

func (b *RecordingBarVisibilityBaseline) Suppress(ctx context.Context) (*RecordingBarVisibilitySuppression, PresentationOutcome, error) {
	if b == nil || b.inner == nil {
		return nil, windowlease.OutcomeUnavailable, ErrPresentationUnavailable
	}
	lease, outcome, err := b.inner.SuppressWithWindowCreatedPrepaint(ctx, wechatRecordingWindow, wechatPrepaintRecordingWindow, visibilityObserveLimit, visibilityPollInterval)
	if lease == nil {
		return nil, outcome, err
	}
	return &RecordingBarVisibilitySuppression{HiddenLease: lease}, outcome, err
}
