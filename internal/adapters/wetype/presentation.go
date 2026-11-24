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
	lease, outcome, err := b.inner.SuppressWithDismissal(ctx, wechatRecordingWindow,
		recordingObserveLimit, recordingPollInterval, recordingStableInterval, restoreWeTypeAfterDismissal)
	if lease != nil && outcome == windowlease.OutcomeSuppressed {
		// The host gets the lease only after this returns, so the observer is
		// ready before a short key-up can start WeType's finish animation.
		_ = lease.StartWindowEvents()
	}
	return lease, outcome, err
}

func restoreWeTypeAfterDismissal(ctx context.Context, lease *windowlease.Lease) error {
	if lease == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	events := lease.WindowEvents()
	if events == nil {
		return lease.WaitForOrderOut(ctx)
	}
	// A window can be ordered out without destroying its AX element.
	// The common move/destroy path wakes by notification.
	fallback := time.NewTicker(2 * time.Second)
	defer fallback.Stop()
	for {
		select {
		case <-ctx.Done():
			return lease.Restore(context.Background())
		case event, ok := <-events:
			if !ok {
				return lease.WaitForOrderOut(ctx)
			}
			switch event {
			case windowlease.WindowEventMoved:
				// AX can move before the compositor updates this window's frame.
				if _, err := lease.ReparkIfReturnedToOriginal(); err != nil {
					return lease.ReleaseWithoutRestore()
				}
			case windowlease.WindowEventDestroyed:
				// The tracked AX element is gone; retire it without querying a
				// potentially reused CGWindowID or moving a replacement window.
				return lease.ReleaseWithoutRestore()
			}
		case <-fallback.C:
			observation, err := lease.ObserveWindow()
			if err != nil || !observation.Found {
				return lease.ReleaseWithoutRestore()
			}
			if !observation.Window.OnScreen {
				return lease.WaitForOrderOut(ctx)
			}
			if !observation.SameGeometry || (!observation.AtOriginal && !observation.AtParked) {
				// A changed shape or user move retires this lease without a write.
				return lease.ReleaseWithoutRestore()
			}
		}
	}
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
