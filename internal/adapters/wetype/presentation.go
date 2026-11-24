package wetype

import (
	"context"
	"time"

	"mimac.local/mimac/internal/adapters/windowlease"
)

type PresentationOutcome = windowlease.Outcome
type RecordingBarSuppression = windowlease.Lease

var (
	ErrPresentationUnavailable = windowlease.ErrUnavailable
	ErrPresentationAmbiguous   = windowlease.ErrAmbiguous
)

const (
	recordingObserveLimit   = 800 * time.Millisecond
	recordingPollInterval   = 20 * time.Millisecond
	recordingStableInterval = 60 * time.Millisecond
	wechatRecordingLayer    = int32(2147483629)
)

type RecordingBarBaseline struct{ inner *windowlease.Baseline }

// CaptureRecordingBarBaseline binds a one-shot window baseline to the exact,
// already-running WeType process selected by the host. Failure leaves its UI
// unchanged. The host scopes the returned baseline to its active voice session.
func CaptureRecordingBarBaseline(pid int32, expectedExecutable string) *RecordingBarBaseline {
	return captureRecordingBarBaselineNative(pid, expectedExecutable)
}

func (b *RecordingBarBaseline) Suppress(ctx context.Context) (*RecordingBarSuppression, PresentationOutcome, error) {
	if b == nil || b.inner == nil {
		return nil, windowlease.OutcomeUnavailable, ErrPresentationUnavailable
	}
	return b.inner.Suppress(ctx, wechatRecordingWindow, recordingObserveLimit, recordingPollInterval, recordingStableInterval)
}

func wechatRecordingWindow(window windowlease.Window) bool {
	return window.OnScreen && window.OwnerPID > 0 && window.Layer == wechatRecordingLayer &&
		window.Frame.Size == (windowlease.Size{Width: 200, Height: 130}) &&
		window.Role == "AXWindow" && window.Subrole == "AXSystemDialog" &&
		window.IdentifierKnown && window.IdentifierEmpty && window.DescriptionKnown && window.DescriptionEmpty &&
		window.ChildrenKnown && len(window.ChildRoles) == 0 &&
		window.AXMatchCount == 1 && window.Element != 0 && window.PositionSettable &&
		window.Position == window.Frame.Origin && window.Size == window.Frame.Size
}
