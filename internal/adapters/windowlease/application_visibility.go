package windowlease

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrVisibilityUnavailable = errors.New("application visibility: accessibility state unavailable")
	ErrVisibilityConflict    = errors.New("application visibility: target UI or focus changed")
)

// ApplicationVisibility contains no window title, value, or editor content.
// ContainsElement is authoritative only after a successful AXWindows read.
type ApplicationVisibility struct {
	Hidden          bool
	WindowCount     int
	ContainsElement bool
	FocusedPID      int32
}

type ApplicationEvent uint8

const (
	ApplicationEventWindowCreated ApplicationEvent = iota + 1
	ApplicationEventShown
)

type ApplicationNotice struct {
	Event   ApplicationEvent
	Element uintptr
}

type ApplicationEventSource interface {
	Events() <-chan ApplicationNotice
	Close()
}

// ApplicationVisibilityProbe is optional so existing position leases retain
// their current native dependencies and behavior.
type ApplicationVisibilityProbe interface {
	ApplicationVisibility(pid int32, element uintptr) (ApplicationVisibility, error)
	SetApplicationHidden(pid int32, hidden bool) error
	WatchApplication(pid int32) (ApplicationEventSource, error)
}

// prepaintWindowProbe is intentionally separate from Probe.Windows: only an
// adapter that opts into prepaint suppression can inspect off-screen CG rows.
type prepaintWindowProbe interface {
	WindowForPrepaint(pid int32, element uintptr) (Window, int, bool, error)
}

type HiddenBaseline struct {
	mu            sync.Mutex
	pid           int32
	checkIdentity func() error
	probe         Probe
	appProbe      ApplicationVisibilityProbe
	focusedPID    int32
	used          bool
}

// CaptureHiddenBaseline only permits an app-wide AXHidden change from a
// visible, empty application baseline. CG covers on-screen windows; AXWindows
// also rejects app windows on another Space or minimized windows.
func CaptureHiddenBaseline(pid int32, checkIdentity func() error, probe Probe) (*HiddenBaseline, error) {
	if pid <= 0 || checkIdentity == nil || probe == nil {
		return nil, ErrVisibilityUnavailable
	}
	appProbe, ok := probe.(ApplicationVisibilityProbe)
	if !ok {
		return nil, ErrVisibilityUnavailable
	}
	if err := checkIdentity(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	ids, err := probe.WindowNumbers(pid)
	if err != nil || len(ids) != 0 {
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrVisibilityUnavailable, err)
		}
		return nil, ErrVisibilityConflict
	}
	state, err := appProbe.ApplicationVisibility(pid, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVisibilityUnavailable, err)
	}
	if state.Hidden || state.WindowCount != 0 || state.FocusedPID <= 0 || state.FocusedPID == pid {
		return nil, ErrVisibilityConflict
	}
	if err := checkIdentity(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	return &HiddenBaseline{pid: pid, checkIdentity: checkIdentity, probe: probe, appProbe: appProbe, focusedPID: state.FocusedPID}, nil
}

// Suppress claims one fresh, fully classified window. A CG window can precede
// its AXWindows entry, so incomplete matches wait for the next event or bounded
// observation rather than being treated as the recording panel.
func (b *HiddenBaseline) Suppress(ctx context.Context, eligible func(Window) bool, observeLimit, pollInterval time.Duration) (*HiddenLease, Outcome, error) {
	return b.suppress(ctx, eligible, nil, observeLimit, pollInterval)
}

// SuppressWithWindowCreatedPrepaint allows a caller with an exact off-screen
// classifier to claim a window only on AXWindowCreated. The regular visible
// discovery path remains unchanged.
func (b *HiddenBaseline) SuppressWithWindowCreatedPrepaint(ctx context.Context, eligible, prepaintEligible func(Window) bool, observeLimit, pollInterval time.Duration) (*HiddenLease, Outcome, error) {
	if prepaintEligible == nil {
		return nil, OutcomeUnavailable, ErrVisibilityUnavailable
	}
	return b.suppress(ctx, eligible, prepaintEligible, observeLimit, pollInterval)
}

func (b *HiddenBaseline) suppress(ctx context.Context, eligible, prepaintEligible func(Window) bool, observeLimit, pollInterval time.Duration) (*HiddenLease, Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if b == nil || eligible == nil || observeLimit <= 0 || pollInterval <= 0 {
		return nil, OutcomeUnavailable, ErrVisibilityUnavailable
	}
	b.mu.Lock()
	if b.used {
		b.mu.Unlock()
		return nil, OutcomeUnavailable, ErrAlreadyUsed
	}
	b.used = true
	b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, OutcomeCancelled, err
	}
	if err := b.checkIdentity(); err != nil {
		return nil, OutcomeUnavailable, fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	events, err := b.appProbe.WatchApplication(b.pid)
	if err != nil || events == nil {
		if err == nil {
			err = ErrVisibilityUnavailable
		}
		return nil, OutcomeUnavailable, fmt.Errorf("%w: %v", ErrVisibilityUnavailable, err)
	}
	transferEvents := false
	defer func() {
		if !transferEvents {
			events.Close()
		}
	}()

	deadline := time.NewTimer(observeLimit)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, OutcomeCancelled, err
		}
		if err := b.checkIdentity(); err != nil {
			return nil, OutcomeUnavailable, fmt.Errorf("%w: %v", ErrIdentityChanged, err)
		}
		windows, err := b.probe.Windows(b.pid, nil)
		if err != nil {
			return nil, OutcomeUnavailable, fmt.Errorf("%w: %v", ErrVisibilityUnavailable, err)
		}
		if len(windows) > 1 {
			releaseWindows(b.probe, windows)
			return nil, OutcomeAmbiguous, ErrAmbiguous
		}
		if len(windows) == 1 {
			window := windows[0]
			if window.OwnerPID != b.pid || !window.OnScreen || window.ID == 0 || window.AXMatchCount > 1 {
				releaseWindows(b.probe, windows)
				return nil, OutcomeAmbiguous, ErrAmbiguous
			}
			if window.AXMatchCount == 1 && window.Element != 0 {
				if !eligible(window) {
					releaseWindows(b.probe, windows)
					return nil, OutcomeUnchanged, nil
				}
				// Pass the sole retained element and observer to the lease.
				transferEvents = true
				return b.hideCandidate(ctx, window, events, OutcomeSuppressed)
			}
			releaseWindows(b.probe, windows)
		}
		select {
		case <-ctx.Done():
			return nil, OutcomeCancelled, ctx.Err()
		case <-deadline.C:
			return nil, OutcomeUnchanged, nil
		case <-ticker.C:
		case notice, ok := <-events.Events():
			if !ok {
				return nil, OutcomeUnavailable, ErrVisibilityUnavailable
			}
			if notice.Event == ApplicationEventWindowCreated && prepaintEligible != nil && notice.Element != 0 {
				lease, outcome, done, consumed, err := b.suppressPrepaintCandidate(ctx, eligible, prepaintEligible, events, notice.Element)
				if consumed {
					transferEvents = true
				}
				if lease != nil {
					transferEvents = true
					return lease, outcome, err
				}
				if !consumed {
					b.probe.Release(notice.Element)
				}
				if err != nil {
					return nil, outcome, err
				}
				if done {
					return nil, outcome, nil
				}
			} else if notice.Element != 0 {
				b.probe.Release(notice.Element)
			}
		}
	}
}

func (b *HiddenBaseline) suppressPrepaintCandidate(ctx context.Context, visibleEligible, prepaintEligible func(Window) bool, events ApplicationEventSource, element uintptr) (*HiddenLease, Outcome, bool, bool, error) {
	probe, ok := b.probe.(prepaintWindowProbe)
	if !ok {
		return nil, OutcomeUnchanged, false, false, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, OutcomeCancelled, true, false, err
	}
	if err := b.checkIdentity(); err != nil {
		return nil, OutcomeUnavailable, true, false, fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	window, onScreenCount, found, err := probe.WindowForPrepaint(b.pid, element)
	if err != nil {
		if errors.Is(err, ErrAmbiguous) {
			return nil, OutcomeAmbiguous, true, false, err
		}
		return nil, OutcomeUnavailable, true, false, fmt.Errorf("%w: %v", ErrVisibilityUnavailable, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, OutcomeCancelled, true, false, err
	}
	if !found || window.ID == 0 || window.Element != element {
		return nil, OutcomeUnchanged, false, false, nil
	}
	windowIsOffscreen := !window.OnScreen && onScreenCount == 0 && prepaintEligible(window)
	windowIsOnscreen := window.OnScreen && onScreenCount == 1 && visibleEligible(window)
	if !windowIsOffscreen && !windowIsOnscreen {
		return nil, OutcomeUnchanged, false, false, nil
	}
	if windowIsOnscreen {
		// The full CG snapshot and event element already match. Let the normal
		// claim perform the fresh unique-ID, AX-membership, and focus checks.
		lease, outcome, err := b.hideCandidate(ctx, window, events, OutcomeSuppressedOnShow)
		return lease, outcome, true, true, err
	}
	if err := b.checkIdentity(); err != nil {
		return nil, OutcomeUnavailable, true, false, fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	current, found, err := b.probe.WindowByID(b.pid, window.ID)
	if err != nil {
		return nil, OutcomeUnavailable, true, false, err
	}
	if !found || !samePrepaintCGWindow(window, current) {
		return nil, OutcomeUnchanged, false, false, nil
	}
	if current.OnScreen {
		visibleWindow := window
		visibleWindow.OnScreen = true
		if !visibleEligible(visibleWindow) {
			return nil, OutcomeUnchanged, false, false, nil
		}
		lease, outcome, err := b.hideCandidate(ctx, visibleWindow, events, OutcomeSuppressedOnShow)
		return lease, outcome, true, true, err
	}
	state, err := b.appProbe.ApplicationVisibility(b.pid, element)
	if err != nil {
		return nil, OutcomeUnavailable, true, false, fmt.Errorf("%w: %v", ErrVisibilityUnavailable, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, OutcomeCancelled, true, false, err
	}
	if state.WindowCount > 1 {
		return nil, OutcomeAmbiguous, true, false, ErrAmbiguous
	}
	if state.Hidden || state.WindowCount != 1 || !state.ContainsElement || state.FocusedPID != b.focusedPID {
		return nil, OutcomeUnchanged, false, false, nil
	}
	if err := b.checkIdentity(); err != nil {
		return nil, OutcomeUnavailable, true, false, fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, OutcomeCancelled, true, false, err
	}
	if err := b.appProbe.SetApplicationHidden(b.pid, true); err != nil {
		return nil, OutcomeUnchanged, true, false, err
	}
	if err := ctx.Err(); err != nil {
		if b.checkIdentity() == nil {
			_ = b.appProbe.SetApplicationHidden(b.pid, false)
		}
		return nil, OutcomeCancelled, true, false, err
	}
	state, err = b.appProbe.ApplicationVisibility(b.pid, element)
	if err != nil {
		if b.checkIdentity() == nil {
			_ = b.appProbe.SetApplicationHidden(b.pid, false)
		}
		return nil, OutcomeUnavailable, true, false, err
	}
	if !state.Hidden {
		return nil, OutcomeUnchanged, false, false, nil
	}
	if state.WindowCount != 1 || !state.ContainsElement || state.FocusedPID != b.focusedPID {
		if b.checkIdentity() == nil {
			_ = b.appProbe.SetApplicationHidden(b.pid, false)
		}
		return nil, OutcomeUnchanged, true, false, nil
	}
	return &HiddenLease{baseline: b, element: element, events: events, hiddenByLease: true}, OutcomeSuppressedPrepaint, true, true, nil
}

func (b *HiddenBaseline) hideCandidate(ctx context.Context, window Window, events ApplicationEventSource, successOutcome Outcome) (result *HiddenLease, outcome Outcome, err error) {
	lease := &HiddenLease{
		baseline: b, element: window.Element, events: events,
	}
	defer func() {
		if result == nil {
			err = errors.Join(err, lease.Restore(context.Background()))
		}
	}()
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, OutcomeCancelled, err
		}
		if err := b.checkIdentity(); err != nil {
			return nil, OutcomeUnavailable, fmt.Errorf("%w: %v", ErrIdentityChanged, err)
		}
		// CG and AX must still agree on the sole window immediately before
		// a process-wide change. The retained element was already classified.
		ids, err := b.probe.WindowNumbers(b.pid)
		if err != nil {
			return nil, OutcomeUnavailable, err
		}
		_, found := ids[window.ID]
		state, err := b.appProbe.ApplicationVisibility(b.pid, window.Element)
		if err != nil {
			return nil, OutcomeUnavailable, err
		}
		if len(ids) != 1 || !found || state.Hidden || !lease.ownsPanel(state) {
			return nil, OutcomeUnchanged, ErrVisibilityConflict
		}
		if err := ctx.Err(); err != nil {
			return nil, OutcomeCancelled, err
		}
		if err := b.appProbe.SetApplicationHidden(b.pid, true); err != nil {
			return nil, OutcomeUnchanged, err
		}
		lease.hiddenByLease = true
		state, err = b.appProbe.ApplicationVisibility(b.pid, window.Element)
		if err != nil {
			return nil, OutcomeUnavailable, err
		}
		if !lease.ownsPanel(state) {
			return nil, OutcomeUnchanged, ErrVisibilityConflict
		}
		if state.Hidden {
			return lease, successOutcome, nil
		}
		// The target can finish showing the very same panel during its first
		// Hidden write. Retry only during this handoff, never during dictation.
		lease.hiddenByLease = false
	}
	return nil, OutcomeUnchanged, nil
}

func samePrepaintCGWindow(a, b Window) bool {
	return a.ID == b.ID && a.OwnerPID == b.OwnerPID && a.Layer == b.Layer && a.Frame == b.Frame
}

type HiddenLease struct {
	mu            sync.Mutex
	baseline      *HiddenBaseline
	element       uintptr
	events        ApplicationEventSource
	hiddenByLease bool
	released      bool
}

func (s *HiddenLease) ownsPanel(state ApplicationVisibility) bool {
	return state.FocusedPID == s.baseline.focusedPID && state.WindowCount == 1 && state.ContainsElement
}

func (s *HiddenLease) IsReleased() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.released
}

// Maintain holds the lease through Fn-up and text delivery. Only the native
// window's dismissal (or a user override) ends visibility ownership.
func (s *HiddenLease) Maintain(ctx context.Context, sessionEnd <-chan struct{}) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	events := s.events.Events()
	var unreadableSince time.Time
	for {
		s.mu.Lock()
		if s.released {
			s.mu.Unlock()
			return nil
		}
		b := s.baseline
		identityErr := b.checkIdentity()
		restore := false
		if identityErr == nil {
			state, err := b.appProbe.ApplicationVisibility(b.pid, s.element)
			// A failed AXWindows query is not evidence that the recorder left.
			if err == nil {
				unreadableSince = time.Time{}
				restore = !state.Hidden || !s.ownsPanel(state)
			} else if unreadableSince.IsZero() {
				unreadableSince = time.Now()
			} else if time.Since(unreadableSince) >= time.Second {
				// Lost inspection capability; give display control back to the
				// input method rather than keeping an unobservable application hidden.
				restore = true
			}
		}
		s.mu.Unlock()
		if identityErr != nil || restore {
			return s.Restore(context.Background())
		}
		select {
		case <-ctx.Done():
			return s.Restore(context.Background())
		case <-sessionEnd:
			sessionEnd = nil
		case notice, ok := <-events:
			if !ok {
				events = nil
			} else if notice.Element != 0 {
				s.baseline.probe.Release(notice.Element)
			}
		case <-ticker.C:
		}
	}
}

func (s *HiddenLease) RestoreAfterDismissal(ctx context.Context) error {
	return s.Maintain(ctx, nil)
}

// Restore immediately returns the original visibility on an explicit opt-out,
// even if the discovery or session context was previously cancelled.
func (s *HiddenLease) Restore(_ context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return nil
	}
	s.released = true
	defer s.events.Close()
	b := s.baseline
	defer b.probe.Release(s.element)
	if !s.hiddenByLease {
		return nil
	}
	var restoreErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := b.checkIdentity(); err != nil {
			return fmt.Errorf("%w: %v", ErrIdentityChanged, err)
		}
		state, err := b.appProbe.ApplicationVisibility(b.pid, s.element)
		if err == nil && !state.Hidden {
			return nil
		}
		restoreErr = b.appProbe.SetApplicationHidden(b.pid, false)
		if restoreErr == nil {
			state, err = b.appProbe.ApplicationVisibility(b.pid, s.element)
			if err == nil && !state.Hidden {
				return nil
			}
			restoreErr = errors.Join(ErrVisibilityUnavailable, err)
		}
	}
	return restoreErr
}
