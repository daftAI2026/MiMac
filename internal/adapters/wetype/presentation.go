package wetype

import (
	"context"
	"math"
	"time"

	"mimac.local/mimac/internal/adapters/windowlease"
)

type PresentationOutcome = windowlease.Outcome

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

// RecordingBarSuppression is the WeType-specific lease policy. The host's
// existing per-session worker calls Maintain for the full session lifetime;
// no second watcher goroutine or session clock is created here.
type RecordingBarSuppression struct {
	*windowlease.Lease
	monitor *weTypeWindowEventMonitor
}

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
	monitor := &weTypeWindowEventMonitor{}
	lease, outcome, err := b.inner.SuppressWithWindowEvents(ctx, wechatRecordingWindow,
		recordingObserveLimit, recordingPollInterval, recordingStableInterval)
	if lease == nil {
		return nil, outcome, err
	}
	return &RecordingBarSuppression{Lease: lease, monitor: monitor}, outcome, err
}

// Maintain is invoked by the host's existing session worker for WeType only.
// It handles qualified AX move/resize reflows throughout capture and after
// key-up, then waits for the same bound window to order out or be destroyed.
func (s *RecordingBarSuppression) Maintain(ctx context.Context, sessionEnd <-chan struct{}) error {
	if s == nil || s.Lease == nil {
		return nil
	}
	monitor := s.monitor
	if monitor == nil {
		monitor = &weTypeWindowEventMonitor{}
		s.monitor = monitor
	}
	return monitor.maintain(ctx, s.Lease, sessionEnd)
}

type weTypeWindowEventMonitor struct {
	knownSizes     map[windowlease.Size]struct{}
	reparkDisabled bool
}

func (m *weTypeWindowEventMonitor) maintain(ctx context.Context, lease *windowlease.Lease, sessionEnd <-chan struct{}) error {
	if lease == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if lease.IsReleased() {
		return nil
	}
	events := lease.WindowEvents()
	if events == nil {
		if sessionEnd == nil {
			return lease.WaitForOrderOut(ctx)
		}
		return lease.Restore(context.Background())
	}
	ended := sessionEnd == nil
	end := sessionEnd
	fallback := time.NewTicker(2 * time.Second)
	defer fallback.Stop()
	for {
		select {
		case <-ctx.Done():
			return lease.Restore(context.Background())
		case <-end:
			ended = true
			end = nil
		case event, ok := <-events:
			if !ok {
				if lease.IsReleased() {
					return nil
				}
				if ended {
					return lease.WaitForOrderOut(ctx)
				}
				return lease.Restore(context.Background())
			}
			switch event {
			case windowlease.WindowEventDestroyed:
				return lease.ReleaseWithoutRestore()
			case windowlease.WindowEventMoved, windowlease.WindowEventResized:
				_, err := lease.ReparkIfLayout(m.layoutDecision)
				if err != nil {
					m.reparkDisabled = true
				}
			}
		case <-fallback.C:
			if !ended {
				continue
			}
			// AX notifications own the active-session path. This low-frequency
			// check only covers targets that order out without destroying AX.
			observation, err := lease.ObserveWindow()
			if err != nil || !observation.Found {
				return lease.ReleaseWithoutRestore()
			}
			if !observation.Window.OnScreen {
				return lease.Restore(ctx)
			}
			if m.reparkDisabled && !observation.AtParked && !observation.AtOriginal {
				// The bound window was already rejected by the adapter's AX
				// identity/geometry policy. Once the session ends, retire it if it
				// remains at an unrecognized point instead of retaining an observer
				// for a user-moved window.
				return lease.ReleaseWithoutRestore()
			}
		}
	}
}

func (m *weTypeWindowEventMonitor) layoutDecision(window windowlease.Window, original, parked windowlease.Point, originalSize windowlease.Size) windowlease.ReparkDecision {
	if !wechatBoundRecordingWindow(window) || !finiteWindowGeometry(window) {
		return m.disableRepark()
	}
	if m.knownSizes == nil {
		m.knownSizes = map[windowlease.Size]struct{}{originalSize: {}}
	}
	centerX := original.X + originalSize.Width/2
	layout := func(origin windowlease.Point, size windowlease.Size) bool {
		if size.Width <= 0 || size.Height != originalSize.Height || math.Abs(origin.Y-original.Y) > 0.01 {
			return false
		}
		return math.Abs(origin.X+size.Width/2-centerX) <= 0.01
	}
	knownSize := func(size windowlease.Size) bool {
		_, ok := m.knownSizes[size]
		return ok && size.Height == originalSize.Height
	}
	axScreen := layout(window.Position, window.Size)
	cgScreen := layout(window.Frame.Origin, window.Frame.Size)
	if axScreen {
		m.knownSizes[window.Size] = struct{}{}
	}
	if cgScreen {
		m.knownSizes[window.Frame.Size] = struct{}{}
	}
	axAtParked := sameWeTypePoint(window.Position, parked)
	cgAtParked := sameWeTypePoint(window.Frame.Origin, parked)
	axParked := axAtParked && knownSize(window.Size)
	cgParked := cgAtParked && knownSize(window.Frame.Size)
	axKnown, cgKnown := axScreen || axParked, cgScreen || cgParked
	if !axKnown && !cgKnown {
		if axAtParked || cgAtParked {
			// A resize can arrive before the corresponding location update while
			// the window is parked. Wait for its paired notification so a later
			// visible layout can be judged by its center and baseline geometry.
			return windowlease.ReparkWait
		}
		return m.disableRepark()
	}
	if !axKnown || !cgKnown {
		// AX and CG can publish the two halves of one native geometry change
		// on adjacent turns. Wait for the paired move/resize notification.
		return windowlease.ReparkWait
	}
	if axScreen && !sameWeTypePoint(window.Position, parked) {
		return windowlease.ReparkNow
	}
	return windowlease.ReparkWait
}

func (m *weTypeWindowEventMonitor) disableRepark() windowlease.ReparkDecision {
	m.reparkDisabled = true
	return windowlease.ReparkDisable
}

func wechatRecordingWindowIdentity(window windowlease.Window) bool {
	return window.OnScreen && window.OwnerPID > 0 && window.Layer == wechatRecordingLayer &&
		window.Role == "AXWindow" && window.Subrole == "AXSystemDialog" &&
		window.IdentifierKnown && window.IdentifierEmpty && window.DescriptionKnown && window.DescriptionEmpty &&
		window.ChildrenKnown && len(window.ChildRoles) == 0 && window.AXMatchCount == 1 &&
		window.Element != 0 && window.PositionSettable
}

// The full role/children/string classifier runs once when the new window is
// claimed. During its lease, the exact retained AX element, owner PID, CG ID,
// layer, and captured writable-position capability are the identity fence.
func wechatBoundRecordingWindow(window windowlease.Window) bool {
	return window.OnScreen && window.OwnerPID > 0 && window.Layer == wechatRecordingLayer &&
		window.AXMatchCount == 1 && window.Element != 0 && window.PositionSettable
}

func finiteWindowGeometry(window windowlease.Window) bool {
	values := []float64{
		window.Frame.Origin.X, window.Frame.Origin.Y, window.Frame.Size.Width, window.Frame.Size.Height,
		window.Position.X, window.Position.Y, window.Size.Width, window.Size.Height,
	}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return window.Frame.Size.Width > 0 && window.Frame.Size.Height > 0 && window.Size.Width > 0 && window.Size.Height > 0
}

func wechatRecordingWindow(window windowlease.Window) bool {
	return wechatRecordingWindowIdentity(window) &&
		window.Frame.Size == (windowlease.Size{Width: 200, Height: 130}) &&
		window.Position == window.Frame.Origin && window.Size == window.Frame.Size
}

func sameWeTypePoint(a, b windowlease.Point) bool { return a.X == b.X && a.Y == b.Y }
