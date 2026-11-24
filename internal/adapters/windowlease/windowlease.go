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
	ElementPID(element uintptr) (int32, error)
	Retain(element uintptr) uintptr
	Release(element uintptr)
}

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
				lease, outcome, err := b.moveWindow(ctx, window)
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

func (b *Baseline) moveWindow(ctx context.Context, window Window) (*Lease, Outcome, error) {
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
	mu            sync.Mutex
	pid           int32
	windowID      uint32
	checkIdentity func() error
	probe         Probe
	element       uintptr
	original      Point
	parked        Point
	released      bool
	restored      bool
}

func (s *Lease) RestoreAfterDismissal(ctx context.Context) error {
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
