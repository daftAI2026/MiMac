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
	WindowEventResized
	WindowEventDestroyed
)

type WindowEventSource interface {
	Events() <-chan WindowEvent
	Close()
}

type windowEventProbe interface {
	WatchWindow(pid int32, element uintptr) (WindowEventSource, error)
}

type boundWindowProbe interface {
	WindowByIDWithAX(pid int32, id uint32, element uintptr) (Window, bool, error)
}

type ReparkDecision uint8

const (
	ReparkWait ReparkDecision = iota
	ReparkNow
	ReparkDisable
)

type ReparkPolicy func(window Window, original, parked Point, originalSize Size) ReparkDecision

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
	return b.suppress(ctx, eligible, observeLimit, pollInterval, stableInterval, false)
}

// SuppressWithWindowEvents registers an exact-element observer before
// parking. The lease owner consumes its event stream for the session.
func (b *Baseline) SuppressWithWindowEvents(ctx context.Context, eligible func(Window) bool, observeLimit, pollInterval, stableInterval time.Duration) (*Lease, Outcome, error) {
	return b.suppress(ctx, eligible, observeLimit, pollInterval, stableInterval, true)
}

func (b *Baseline) suppress(ctx context.Context, eligible func(Window) bool, observeLimit, pollInterval, stableInterval time.Duration, watchWindowEvents bool) (*Lease, Outcome, error) {
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
				lease, outcome, err := b.moveWindow(ctx, window, watchWindowEvents)
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

func (b *Baseline) moveWindow(ctx context.Context, window Window, watchWindowEvents bool) (*Lease, Outcome, error) {
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
		layer: window.Layer, frameSize: window.Frame.Size,
	}
	if watchWindowEvents {
		if err := lease.startWindowEvents(); err != nil {
			_ = lease.release()
			return nil, OutcomeUnavailable, err
		}
	}
	if err := ctx.Err(); err != nil {
		_ = lease.release()
		return nil, OutcomeCancelled, err
	}
	if err := b.probe.SetPosition(window.Element, parked); err != nil {
		rollbackErr := lease.rollbackUnconfirmedInitialPark()
		return nil, OutcomeUnchanged, errors.Join(fmt.Errorf("recording window: AXPosition set failed: %w", err), rollbackErr)
	}
	confirmed, err := b.probe.Position(window.Element)
	if err != nil || !samePoint(confirmed, parked) {
		rollbackErr := lease.rollbackUnconfirmedInitialPark()
		return nil, OutcomeUnchanged, errors.Join(errors.New("recording window: AXPosition write was not confirmed"), err, rollbackErr)
	}
	return lease, OutcomeSuppressed, nil
}

// rollbackUnconfirmedInitialPark closes a prepared event source without
// enabling its handler. It restores only a partial initial move of the same
// element while the captured geometry is still intact.
func (s *Lease) rollbackUnconfirmedInitialPark() (result error) {
	if s == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer func() {
		probe, element, source := s.detachLocked()
		s.mu.Unlock()
		if source != nil {
			source.Close()
		}
		if probe != nil && element != 0 {
			probe.Release(element)
		}
	}()
	if s.element == 0 || s.probe == nil || s.checkIdentity == nil {
		return ErrUnavailable
	}
	if err := s.checkIdentity(); err != nil {
		return fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	window, found, err := s.probe.WindowByID(s.pid, s.windowID)
	if err != nil || !found || window.OwnerPID != s.pid || window.ID != s.windowID ||
		!window.OnScreen || window.Layer != s.layer || !sameSize(window.Frame.Size, s.frameSize) ||
		(!samePoint(window.Frame.Origin, s.original) && !samePoint(window.Frame.Origin, s.parked)) {
		if err != nil {
			return fmt.Errorf("%w: %v", ErrWindowChanged, err)
		}
		return ErrWindowChanged
	}
	pid, err := s.probe.ElementPID(s.element)
	if err != nil || pid != s.pid {
		if err != nil {
			return fmt.Errorf("%w: %v", ErrWindowChanged, err)
		}
		return ErrWindowChanged
	}
	position, err := s.probe.Position(s.element)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWindowChanged, err)
	}
	size, err := s.probe.Size(s.element)
	if err != nil || !sameSize(size, s.frameSize) {
		if err != nil {
			return fmt.Errorf("%w: %v", ErrWindowChanged, err)
		}
		return ErrWindowChanged
	}
	if samePoint(position, s.original) {
		return nil
	}
	if !samePoint(position, s.parked) {
		return ErrMovedByUser
	}
	if err := s.checkIdentity(); err != nil {
		return fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	if err := s.probe.SetPosition(s.element, s.original); err != nil {
		return fmt.Errorf("recording window: initial move rollback failed: %w", err)
	}
	confirmed, err := s.probe.Position(s.element)
	if err != nil || !samePoint(confirmed, s.original) {
		if err != nil {
			return fmt.Errorf("%w: %v", ErrWindowChanged, err)
		}
		return errors.New("recording window: initial move rollback was not confirmed")
	}
	s.restored = true
	return nil
}

func releaseWindows(probe Probe, windows []Window) {
	for _, window := range windows {
		if window.Element != 0 {
			probe.Release(window.Element)
		}
	}
}

type Lease struct {
	mu             sync.Mutex
	pid            int32
	windowID       uint32
	checkIdentity  func() error
	probe          Probe
	element        uintptr
	original       Point
	parked         Point
	layer          int32
	frameSize      Size
	reparkDisabled bool
	eventSource    WindowEventSource
	released       bool
	restored       bool
}

// LeaseWindowObservation contains only the exact CG window's identity and
// geometry for the low-frequency orderOut fallback.
type LeaseWindowObservation struct {
	Window     Window
	Found      bool
	AtOriginal bool
	AtParked   bool
}

// ObserveWindow performs only the exact CG window lookup; it never queries AX.
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
		Window:     window,
		Found:      true,
		AtOriginal: samePoint(window.Frame.Origin, s.original),
		AtParked:   samePoint(window.Frame.Origin, s.parked),
	}, nil
}

// ReparkIfLayout applies one adapter-approved move/resize transition to the
// exact retained AX element. The caller decides which layouts are automatic;
// this lease supplies the PID/window fence and the captured original point.
func (s *Lease) ReparkIfLayout(policy ReparkPolicy) (bool, error) {
	if s == nil {
		return false, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released || s.restored || s.element == 0 || s.probe == nil || s.checkIdentity == nil || policy == nil {
		return false, ErrUnavailable
	}
	if s.reparkDisabled {
		return false, nil
	}
	validator, ok := s.probe.(boundWindowProbe)
	if !ok {
		s.reparkDisabled = true
		return false, ErrUnavailable
	}
	window, found, err := validator.WindowByIDWithAX(s.pid, s.windowID, s.element)
	if err != nil {
		s.reparkDisabled = true
		return false, fmt.Errorf("%w: %v", ErrWindowChanged, err)
	}
	if !found || window.OwnerPID != s.pid || window.ID != s.windowID {
		s.reparkDisabled = true
		return false, ErrWindowChanged
	}
	if !window.OnScreen {
		return false, nil
	}
	if window.Layer != s.layer {
		s.reparkDisabled = true
		return false, ErrWindowChanged
	}
	pid, err := s.probe.ElementPID(s.element)
	if err != nil || pid != s.pid {
		s.reparkDisabled = true
		if err != nil {
			return false, fmt.Errorf("%w: %v", ErrWindowChanged, err)
		}
		return false, ErrWindowChanged
	}
	position, err := s.probe.Position(s.element)
	if err != nil {
		s.reparkDisabled = true
		return false, fmt.Errorf("%w: %v", ErrWindowChanged, err)
	}
	size, err := s.probe.Size(s.element)
	if err != nil {
		s.reparkDisabled = true
		return false, fmt.Errorf("%w: %v", ErrWindowChanged, err)
	}
	if !samePoint(position, window.Position) || !sameSize(size, window.Size) {
		// AX and CG can publish a geometry change on adjacent run-loop turns.
		// Wait for the next native notification rather than classifying this
		// one-frame mismatch as a user move.
		return false, nil
	}
	if err := s.checkIdentity(); err != nil {
		s.reparkDisabled = true
		return false, fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	switch policy(window, s.original, s.parked, s.frameSize) {
	case ReparkWait:
		return false, nil
	case ReparkDisable:
		s.reparkDisabled = true
		return false, nil
	case ReparkNow:
		if samePoint(position, s.parked) {
			return false, nil
		}
	default:
		s.reparkDisabled = true
		return false, ErrUnavailable
	}
	if !window.PositionSettable {
		s.reparkDisabled = true
		return false, ErrUnavailable
	}
	if err := s.checkIdentity(); err != nil {
		s.reparkDisabled = true
		return false, fmt.Errorf("%w: %v", ErrIdentityChanged, err)
	}
	if err := s.probe.SetPosition(s.element, s.parked); err != nil {
		s.reparkDisabled = true
		return false, fmt.Errorf("recording window: AXPosition repark failed: %w", err)
	}
	confirmed, err := s.probe.Position(s.element)
	if err != nil || !samePoint(confirmed, s.parked) {
		s.reparkDisabled = true
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
	return s.WaitForOrderOut(ctx)
}

// startWindowEvents subscribes before the first AX write. The session owner
// consumes this exact retained element's events in its existing worker.
func (s *Lease) startWindowEvents() error {
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

func (s *Lease) IsReleased() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.released
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
	if s.restored {
		s.mu.Unlock()
		return nil
	}
	defer func() {
		probe, element, source := s.detachLocked()
		s.mu.Unlock()
		if source != nil {
			source.Close()
		}
		if probe != nil && element != 0 {
			probe.Release(element)
		}
	}()
	if s.probe == nil || s.checkIdentity == nil || s.element == 0 {
		return ErrUnavailable
	}
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
	probe, element, source := s.detachLocked()
	s.mu.Unlock()
	if source != nil {
		source.Close()
	}
	if probe != nil && element != 0 {
		probe.Release(element)
	}
	return nil
}

// detachLocked drops all lease ownership before native Close joins its runloop
// source. Event handlers may re-enter the lease and must never wait behind a
// mutex held by that join.
func (s *Lease) detachLocked() (Probe, uintptr, WindowEventSource) {
	if s.released {
		return nil, 0, nil
	}
	probe, element, source := s.probe, s.element, s.eventSource
	s.eventSource = nil
	s.element = 0
	s.released = true
	return probe, element, source
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
