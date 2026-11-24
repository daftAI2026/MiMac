package windowlease

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

var (
	ErrUnavailable     = errors.New("recording window: accessibility or window metadata unavailable")
	ErrAmbiguous       = errors.New("recording window: more than one new window")
	ErrIdentityChanged = errors.New("recording window: process identity changed")
	ErrWindowChanged   = errors.New("recording window: window identity changed")
	ErrMovedByUser     = errors.New("recording window: position changed externally")
	ErrAlreadyUsed     = errors.New("recording window: baseline already consumed")
)

type Outcome string

const (
	OutcomeUnchanged   Outcome = "unchanged"
	OutcomeSuppressed  Outcome = "suppressed"
	OutcomeAmbiguous   Outcome = "ambiguous"
	OutcomeCancelled   Outcome = "cancelled"
	OutcomeUnavailable Outcome = "unavailable"
)

type Point struct{ X, Y float64 }
type Size struct{ Width, Height float64 }
type Rect struct {
	Origin Point
	Size   Size
}

// Window contains only window and accessibility identity metadata. It never
// includes AXValue, title, or application content.
type Window struct {
	ID               uint32
	OwnerPID         int32
	Layer            int32
	Frame            Rect
	OnScreen         bool
	Role             string
	Subrole          string
	IdentifierEmpty  bool
	IdentifierKnown  bool
	DescriptionEmpty bool
	DescriptionKnown bool
	Element          uintptr
	Position         Point
	Size             Size
	AXMatchCount     int
	PositionSettable bool
	ChildrenKnown    bool
	ChildRoles       []string
}

type Probe interface {
	WindowNumbers(pid int32) (map[uint32]struct{}, error)
	Windows(pid int32, newerThan map[uint32]struct{}) ([]Window, error)
	WindowByID(pid int32, id uint32) (Window, bool, error)
	SetPosition(element uintptr, point Point) error
	Position(element uintptr) (Point, error)
	Size(element uintptr) (Size, error)
	ElementPID(element uintptr) (int32, error)
	Retain(element uintptr) uintptr
	Release(element uintptr)
}

type WindowEvent uint8

const (
	WindowEventMoved WindowEvent = iota + 1
	WindowEventDestroyed
)

type WindowEventSource interface {
	Events() <-chan WindowEvent
	Close()
}

type windowEventProbe interface {
	WatchWindow(pid int32, element uintptr) (WindowEventSource, error)
}

type DismissalHandler func(context.Context, *Lease) error

type Baseline struct {
	mu            sync.Mutex
	pid           int32
	checkIdentity func() error
	probe         Probe
	existing      map[uint32]struct{}
	used          bool
}

// CaptureBaseline snapshots visible window numbers for one already running
// process after checking its caller-supplied identity fence.
func CaptureBaseline(pid int32, checkIdentity func() error, probe Probe) (*Baseline, error) {
	if pid <= 0 || checkIdentity == nil || probe == nil {
		return nil, ErrUnavailable
	}
	if err := checkIdentity(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	existing, err := probe.WindowNumbers(pid)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := checkIdentity(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	copyOfIDs := make(map[uint32]struct{}, len(existing))
	for id := range existing {
		if id != 0 {
			copyOfIDs[id] = struct{}{}
		}
	}
	return &Baseline{pid: pid, checkIdentity: checkIdentity, probe: probe, existing: copyOfIDs}, nil
}

func (b *Baseline) Suppress(ctx context.Context, eligible func(Window) bool, observeLimit, pollInterval, stableInterval time.Duration) (*Lease, Outcome, error) {
	return b.suppress(ctx, eligible, observeLimit, pollInterval, stableInterval, nil)
}

// SuppressWithDismissal lets one adapter provide a tool-specific normal-end
// policy while sharing the same identity-fenced lease and immediate Restore.
func (b *Baseline) SuppressWithDismissal(ctx context.Context, eligible func(Window) bool, observeLimit, pollInterval, stableInterval time.Duration, dismissal DismissalHandler) (*Lease, Outcome, error) {
	return b.suppress(ctx, eligible, observeLimit, pollInterval, stableInterval, dismissal)
}

func (b *Baseline) suppress(ctx context.Context, eligible func(Window) bool, observeLimit, pollInterval, stableInterval time.Duration, dismissal DismissalHandler) (*Lease, Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if b == nil || eligible == nil {
		return nil, OutcomeUnavailable, ErrUnavailable
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
	if observeLimit <= 0 || pollInterval <= 0 || stableInterval < 0 {
		return nil, OutcomeUnavailable, ErrUnavailable
	}
	deadline := time.NewTimer(observeLimit)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var candidateID uint32
	var stableSince time.Time
	for {
		if err := ctx.Err(); err != nil {
			return nil, OutcomeCancelled, err
		}
		if err := b.checkIdentity(); err != nil {
			return nil, OutcomeUnavailable, fmt.Errorf("%w: %v", ErrIdentityChanged, err)
		}
		windows, err := b.probe.Windows(b.pid, b.existing)
		if err != nil {
			return nil, OutcomeUnavailable, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		newWindows := make([]Window, 0, 2)
		for _, window := range windows {
			if window.OwnerPID != b.pid || window.ID == 0 {
				continue
			}
			if _, old := b.existing[window.ID]; old {
				continue
			}
			newWindows = append(newWindows, window)
		}
		if len(newWindows) > 1 {
			releaseWindows(b.probe, newWindows)
			return nil, OutcomeAmbiguous, ErrAmbiguous
		}
		if len(newWindows) == 1 {
			window := newWindows[0]
			if !eligible(window) {
				releaseWindows(b.probe, newWindows)
				candidateID, stableSince = 0, time.Time{}
			} else if candidateID != window.ID {
				releaseWindows(b.probe, newWindows)
				candidateID, stableSince = window.ID, time.Now()
			} else if time.Since(stableSince) >= stableInterval {
				lease, outcome, err := b.moveWindow(ctx, window, dismissal)
				releaseWindows(b.probe, newWindows)
				return lease, outcome, err
			} else {
				releaseWindows(b.probe, newWindows)
			}
		} else {
			candidateID, stableSince = 0, time.Time{}
		}
		select {
		case <-ctx.Done():
			return nil, OutcomeCancelled, ctx.Err()
		case <-deadline.C:
			return nil, OutcomeUnchanged, nil
		case <-ticker.C:
		}
	}
}

func (b *Baseline) moveWindow(ctx context.Context, window Window, dismissal DismissalHandler) (*Lease, Outcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, OutcomeCancelled, err
	}
	if err := b.checkIdentity(); err != nil {
		return nil, OutcomeUnavailable, fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	current, found, err := b.probe.WindowByID(b.pid, window.ID)
	if err != nil || !found || current.OwnerPID != b.pid || !current.OnScreen ||
		current.Layer != window.Layer || !sameRect(current.Frame, window.Frame) {
		if err == nil {
			err = ErrWindowChanged
		}
		return nil, OutcomeUnchanged, fmt.Errorf("%w: %v", ErrWindowChanged, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, OutcomeCancelled, err
	}
	if err := b.checkIdentity(); err != nil {
		return nil, OutcomeUnavailable, fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	position, err := b.probe.Position(window.Element)
	if err != nil || !samePoint(position, window.Position) {
		if err == nil {
			err = ErrWindowChanged
		}
		return nil, OutcomeUnchanged, fmt.Errorf("%w: %v", ErrWindowChanged, err)
	}
	pid, err := b.probe.ElementPID(window.Element)
	if err != nil || pid != b.pid {
		if err == nil {
			err = ErrWindowChanged
		}
		return nil, OutcomeUnchanged, fmt.Errorf("%w: %v", ErrWindowChanged, err)
	}
	if !finitePoint(position) {
		return nil, OutcomeUnchanged, ErrWindowChanged
	}
	parked := Point{X: -1000000, Y: -1000000}
	if samePoint(position, parked) {
		return nil, OutcomeUnchanged, ErrWindowChanged
	}
	retained := b.probe.Retain(window.Element)
	if retained == 0 {
		return nil, OutcomeUnavailable, ErrUnavailable
	}
	lease := &Lease{
		pid: b.pid, windowID: window.ID, checkIdentity: b.checkIdentity,
		probe: b.probe, element: retained, original: position, parked: parked,
		layer: window.Layer, frameSize: window.Frame.Size, dismissal: dismissal,
	}
	if err := ctx.Err(); err != nil {
		_ = lease.release()
		return nil, OutcomeCancelled, err
	}
	if err := b.probe.SetPosition(window.Element, parked); err != nil {
		return lease, OutcomeUnchanged, fmt.Errorf("recording window: AXPosition set failed: %w", err)
	}
	confirmed, err := b.probe.Position(window.Element)
	if err != nil || !samePoint(confirmed, parked) {
		return lease, OutcomeUnchanged, errors.New("recording window: AXPosition write was not confirmed")
	}
	return lease, OutcomeSuppressed, nil
}

func releaseWindows(probe Probe, windows []Window) {
	for _, window := range windows {
		if window.Element != 0 {
			probe.Release(window.Element)
		}
	}
}

type Lease struct {
	mu              sync.Mutex
	pid             int32
	windowID        uint32
	checkIdentity   func() error
	probe           Probe
	element         uintptr
	original        Point
	parked          Point
	layer           int32
	frameSize       Size
	reparkAttempted bool
	dismissal       DismissalHandler
	eventSource     WindowEventSource
	released        bool
	restored        bool
}

// LeaseWindowObservation contains CG identity and geometry only. Callers use it
// to decide whether an expensive AX recheck is needed.
type LeaseWindowObservation struct {
	Window       Window
	Found        bool
	AtOriginal   bool
	AtParked     bool
	SameGeometry bool
}

// ObserveWindow performs only the exact CG window lookup. AX is read later only
// when the window has visibly returned to its original position.
func (s *Lease) ObserveWindow() (LeaseWindowObservation, error) {
	if s == nil {
		return LeaseWindowObservation{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released || s.element == 0 || s.probe == nil {
		return LeaseWindowObservation{}, ErrUnavailable
	}
	window, found, err := s.probe.WindowByID(s.pid, s.windowID)
	if err != nil {
		return LeaseWindowObservation{}, fmt.Errorf("%w: %v", ErrWindowChanged, err)
	}
	if !found {
		return LeaseWindowObservation{}, nil
	}
	if window.ID != s.windowID || window.OwnerPID != s.pid {
		return LeaseWindowObservation{}, ErrWindowChanged
	}
	return LeaseWindowObservation{
		Window: window, Found: true,
		AtOriginal:   samePoint(window.Frame.Origin, s.original),
		AtParked:     samePoint(window.Frame.Origin, s.parked),
		SameGeometry: window.Layer == s.layer && sameSize(window.Frame.Size, s.frameSize),
	}, nil
}

// ReparkIfReturnedToOriginal performs at most one WeType-compatible repair:
// it writes the parked point only after the same window and process have
// explicitly returned to the captured original point with unchanged geometry.
func (s *Lease) ReparkIfReturnedToOriginal() (bool, error) {
	if s == nil {
		return false, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released || s.restored || s.element == 0 || s.probe == nil || s.checkIdentity == nil {
		return false, ErrUnavailable
	}
	if s.reparkAttempted {
		return false, nil
	}
	window, found, err := s.probe.WindowByID(s.pid, s.windowID)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrWindowChanged, err)
	}
	if !found || window.OwnerPID != s.pid || window.ID != s.windowID {
		return false, ErrWindowChanged
	}
	currentCGOriginIsKnown := samePoint(window.Frame.Origin, s.original) || samePoint(window.Frame.Origin, s.parked)
	if !window.OnScreen || window.Layer != s.layer || !sameSize(window.Frame.Size, s.frameSize) || !currentCGOriginIsKnown {
		return false, nil
	}
	pid, err := s.probe.ElementPID(s.element)
	if err != nil || pid != s.pid {
		if err != nil {
			return false, fmt.Errorf("%w: %v", ErrWindowChanged, err)
		}
		return false, ErrWindowChanged
	}
	position, err := s.probe.Position(s.element)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrWindowChanged, err)
	}
	size, err := s.probe.Size(s.element)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrWindowChanged, err)
	}
	if !samePoint(position, s.original) || !sameSize(size, s.frameSize) {
		return false, nil
	}
	if err := s.checkIdentity(); err != nil {
		return false, fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	s.reparkAttempted = true
	if err := s.probe.SetPosition(s.element, s.parked); err != nil {
		return false, fmt.Errorf("recording window: AXPosition repark failed: %w", err)
	}
	confirmed, err := s.probe.Position(s.element)
	if err != nil || !samePoint(confirmed, s.parked) {
		if err != nil {
			return false, fmt.Errorf("%w: %v", ErrWindowChanged, err)
		}
		return false, errors.New("recording window: AXPosition repark was not confirmed")
	}
	return true, nil
}

func (s *Lease) RestoreAfterDismissal(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	dismissal := s.dismissal
	s.mu.Unlock()
	if dismissal != nil {
		return dismissal(ctx, s)
	}
	return s.WaitForOrderOut(ctx)
}

// StartWindowEvents subscribes to notifications on the exact retained AX
// element. Adapters call this before returning the lease to the host so the
// subscription is ready before key-up.
func (s *Lease) StartWindowEvents() error {
	if s == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released || s.element == 0 || s.probe == nil || s.checkIdentity == nil {
		return ErrUnavailable
	}
	if s.eventSource != nil {
		return nil
	}
	provider, ok := s.probe.(windowEventProbe)
	if !ok {
		return ErrUnavailable
	}
	if err := s.checkIdentity(); err != nil {
		return fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	pid, err := s.probe.ElementPID(s.element)
	if err != nil || pid != s.pid {
		if err != nil {
			return fmt.Errorf("%w: %v", ErrWindowChanged, err)
		}
		return ErrWindowChanged
	}
	source, err := provider.WatchWindow(s.pid, s.element)
	if err != nil {
		return err
	}
	if source == nil {
		return ErrUnavailable
	}
	if err := s.checkIdentity(); err != nil {
		source.Close()
		return fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	s.eventSource = source
	return nil
}

func (s *Lease) WindowEvents() <-chan WindowEvent {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventSource == nil {
		return nil
	}
	return s.eventSource.Events()
}

// ReleaseWithoutRestore retires a lease after its observed AX element is
// destroyed. It does not touch a later window that may reuse the CG number.
func (s *Lease) ReleaseWithoutRestore() error { return s.release() }

// WaitForOrderOut is the shared low-frequency CG fallback used after a normal
// end. It restores only while the captured window still owns the parked point.
func (s *Lease) WaitForOrderOut(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	interval := 40 * time.Millisecond
	for {
		if err := s.checkIdentity(); err != nil {
			return s.release()
		}
		w, found, err := s.probe.WindowByID(s.pid, s.windowID)
		if err != nil {
			_ = s.release()
			return err
		}
		if !found {
			return s.release()
		}
		if !w.OnScreen {
			return s.Restore(ctx)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return s.Restore(context.Background())
		case <-timer.C:
		}
		interval = min(interval*2, 500*time.Millisecond)
	}
}

func (s *Lease) Restore(ctx context.Context) error {
	if s == nil {
		return nil
	}
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.restored {
		return nil
	}
	if s.probe == nil || s.checkIdentity == nil || s.element == 0 {
		return ErrUnavailable
	}
	defer s.releaseLocked()
	if err := s.checkIdentity(); err != nil {
		return fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	window, found, err := s.probe.WindowByID(s.pid, s.windowID)
	if err != nil || !found || window.OwnerPID != s.pid {
		if err != nil {
			return fmt.Errorf("%w: %v", ErrWindowChanged, err)
		}
		return ErrWindowChanged
	}
	if !samePoint(window.Frame.Origin, s.parked) {
		return ErrMovedByUser
	}
	pid, err := s.probe.ElementPID(s.element)
	if err != nil || pid != s.pid {
		if err != nil {
			return fmt.Errorf("%w: %v", ErrWindowChanged, err)
		}
		return ErrWindowChanged
	}
	position, err := s.probe.Position(s.element)
	if err != nil || !samePoint(position, s.parked) {
		if err != nil {
			return fmt.Errorf("%w: %v", ErrWindowChanged, err)
		}
		return ErrMovedByUser
	}
	if err := s.checkIdentity(); err != nil {
		return fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	if err := s.probe.SetPosition(s.element, s.original); err != nil {
		return fmt.Errorf("recording window: AXPosition restore failed: %w", err)
	}
	restored, err := s.probe.Position(s.element)
	if err != nil || !samePoint(restored, s.original) {
		return errors.New("recording window: AXPosition restore was not confirmed")
	}
	s.restored = true
	return nil
}

func (s *Lease) release() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.releaseLocked()
}

func (s *Lease) releaseLocked() error {
	if s.released {
		return nil
	}
	if s.eventSource != nil {
		s.eventSource.Close()
		s.eventSource = nil
	}
	s.probe.Release(s.element)
	s.element = 0
	s.released = true
	return nil
}

func samePoint(a, b Point) bool { return a.X == b.X && a.Y == b.Y }
func sameSize(a, b Size) bool   { return a.Width == b.Width && a.Height == b.Height }
func sameRect(a, b Rect) bool   { return samePoint(a.Origin, b.Origin) && sameSize(a.Size, b.Size) }
func finitePoint(value Point) bool {
	return !math.IsNaN(value.X) && !math.IsNaN(value.Y) && !math.IsInf(value.X, 0) && !math.IsInf(value.Y, 0)
}
func finiteSize(value Size) bool {
	return !math.IsNaN(value.Width) && !math.IsNaN(value.Height) && !math.IsInf(value.Width, 0) && !math.IsInf(value.Height, 0)
}
