package doubao

import (
	"context"
	"time"

	"mimac.local/mimac/internal/adapters/windowlease"
)

var (
	ErrPresentationUnavailable     = windowlease.ErrUnavailable
	ErrPresentationAmbiguous       = windowlease.ErrAmbiguous
	ErrPresentationIdentityChanged = windowlease.ErrIdentityChanged
	ErrPresentationWindowChanged   = windowlease.ErrWindowChanged
	ErrPresentationMovedByUser     = windowlease.ErrMovedByUser
	ErrPresentationAlreadyUsed     = windowlease.ErrAlreadyUsed
)

type Outcome = windowlease.Outcome
type Suppression = windowlease.Lease

const (
	OutcomeUnchanged   = windowlease.OutcomeUnchanged
	OutcomeSuppressed  = windowlease.OutcomeSuppressed
	OutcomeAmbiguous   = windowlease.OutcomeAmbiguous
	OutcomeCancelled   = windowlease.OutcomeCancelled
	OutcomeUnavailable = windowlease.OutcomeUnavailable
)

type RecordingBarBaseline struct{ inner *windowlease.Baseline }

const (
	presentationObserveLimit = 800 * time.Millisecond
	presentationPollInterval = 20 * time.Millisecond
)

// CaptureRecordingBarBaseline snapshots IDs from the exact, already-running
// Doubao executable selected by the host. The caller must capture immediately
// before the existing voice shortcut. Failure leaves Doubao's display alone.
func CaptureRecordingBarBaseline(pid int32, expectedExecutable string) *RecordingBarBaseline {
	return captureRecordingBarBaselineNative(pid, expectedExecutable)
}

func captureRecordingBarBaselineWithProbe(pid int32, checkIdentity func() error, probe windowlease.Probe) (*RecordingBarBaseline, error) {
	baseline, err := windowlease.CaptureBaseline(pid, checkIdentity, probe)
	if err != nil {
		return nil, err
	}
	return &RecordingBarBaseline{inner: baseline}, nil
}

func (b *RecordingBarBaseline) Suppress(ctx context.Context) (*Suppression, Outcome, error) {
	if b == nil || b.inner == nil {
		return nil, OutcomeUnavailable, ErrPresentationUnavailable
	}
	return b.inner.Suppress(ctx, doubaoRecordingWindow, presentationObserveLimit, presentationPollInterval, 0)
}

func doubaoRecordingWindow(window windowlease.Window) bool {
	childrenMatch := window.ChildrenKnown && len(window.ChildRoles) == 2 &&
		window.ChildRoles[0] == "AXStaticText" && window.ChildRoles[1] == "AXStaticText"
	return window.OnScreen && window.Layer == 3 && window.Role == "AXWindow" && window.Subrole == "AXSystemDialog" &&
		window.Frame.Size == (windowlease.Size{Width: 118, Height: 32}) && window.AXMatchCount == 1 && childrenMatch &&
		window.Element != 0 && window.PositionSettable && window.Position == window.Frame.Origin && window.Size == window.Frame.Size
}
