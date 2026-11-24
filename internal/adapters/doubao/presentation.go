package doubao

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

var (
	ErrPresentationUnavailable     = errors.New("doubao presentation: accessibility or window metadata unavailable")
	ErrPresentationAmbiguous       = errors.New("doubao presentation: more than one new window")
	ErrPresentationIdentityChanged = errors.New("doubao presentation: process identity changed")
	ErrPresentationWindowChanged   = errors.New("doubao presentation: recording window changed")
	ErrPresentationMovedByUser     = errors.New("doubao presentation: window position changed externally")
	ErrPresentationAlreadyUsed     = errors.New("doubao presentation: baseline already consumed")
)

// Outcome is a short, non-sensitive diagnostic value for the host.
type Outcome string

const (
	OutcomeUnchanged   Outcome = "unchanged"
	OutcomeSuppressed  Outcome = "suppressed"
	OutcomeAmbiguous   Outcome = "ambiguous"
	OutcomeCancelled   Outcome = "cancelled"
	OutcomeUnavailable Outcome = "unavailable"
)

type presentationPoint struct{ x, y float64 }
type presentationSize struct{ width, height float64 }
type presentationRect struct {
	origin presentationPoint
	size   presentationSize
}

type presentationWindow struct {
	id                   uint32
	ownerPID             int32
	layer                int32
	frame                presentationRect
	onScreen             bool
	role                 string
	subrole              string
	element              uintptr
	position             presentationPoint
	size                 presentationSize
	axMatchCount         int
	positionSettable     bool
	recordingBarChildren bool
}

type presentationProbe interface {
	windowNumbers(pid int32) (map[uint32]struct{}, error)
	windows(pid int32, newerThan map[uint32]struct{}) ([]presentationWindow, error)
	windowByID(pid int32, id uint32) (presentationWindow, bool, error)
	setPosition(element uintptr, point presentationPoint) error
	position(element uintptr) (presentationPoint, error)
	elementPID(element uintptr) (int32, error)
	retain(element uintptr) uintptr
	release(element uintptr)
}

type RecordingBarBaseline struct {
	mu            sync.Mutex
	pid           int32
	checkIdentity func() error
	probe         presentationProbe
	existing      map[uint32]struct{}

	used bool
}

// CaptureRecordingBarBaseline snapshots window IDs from the exact, already
// running Doubao executable selected by the host. The caller must capture this
// immediately before sending the existing voice shortcut. Failure returns nil
// and leaves Doubao's display unchanged. It never starts the app, queries text,
// or requests Accessibility permission.
func CaptureRecordingBarBaseline(pid int32, expectedExecutable string) *RecordingBarBaseline {
	return captureRecordingBarBaselineNative(pid, expectedExecutable)
}

func captureRecordingBarBaseline(pid int32, checkIdentity func() error, probe presentationProbe) (*RecordingBarBaseline, error) {
	if pid <= 0 || checkIdentity == nil || probe == nil {
		return nil, ErrPresentationUnavailable
	}
	if err := checkIdentity(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPresentationIdentityChanged, err)
	}
	existing, err := probe.windowNumbers(pid)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPresentationUnavailable, err)
	}
	if err := checkIdentity(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPresentationIdentityChanged, err)
	}
	copyOfIDs := make(map[uint32]struct{}, len(existing))
	for id := range existing {
		if id != 0 {
			copyOfIDs[id] = struct{}{}
		}
	}
	return &RecordingBarBaseline{
		pid: pid, checkIdentity: checkIdentity, probe: probe,
		existing: copyOfIDs,
	}, nil
}

// Suppress observes only a short session-start interval, after the host has
// verified that the selected Doubao route is capturing and MiMac's animation
// is active. It considers only new windows and requires the compact recorder layout, two static-text
// children, exactly one CG/AX window match and a writable position. It never
// reads AXValue, AXTitle, or transcript text. Call on a worker.
func (b *RecordingBarBaseline) Suppress(ctx context.Context) (*Suppression, Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if b == nil {
		return nil, OutcomeUnavailable, ErrPresentationUnavailable
	}
	b.mu.Lock()
	if b.used {
		b.mu.Unlock()
		return nil, OutcomeUnavailable, ErrPresentationAlreadyUsed
	}
	b.used = true
	b.mu.Unlock()
	return b.suppress(ctx, presentationObserveLimit, presentationPollInterval, presentationStableInterval)
}

const (
	presentationObserveLimit   = 800 * time.Millisecond
	presentationPollInterval   = 20 * time.Millisecond
	presentationStableInterval = 60 * time.Millisecond
)

func (b *RecordingBarBaseline) suppress(ctx context.Context, observeLimit, pollInterval, stableInterval time.Duration) (*Suppression, Outcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, OutcomeCancelled, err
	}
	if err := b.checkIdentity(); err != nil {
		return nil, OutcomeUnavailable, fmt.Errorf("%w: %v", ErrPresentationIdentityChanged, err)
	}
	if observeLimit <= 0 || pollInterval <= 0 || stableInterval < 0 {
		return nil, OutcomeUnavailable, ErrPresentationUnavailable
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
			return nil, OutcomeUnavailable, fmt.Errorf("%w: %v", ErrPresentationIdentityChanged, err)
		}
		windows, err := b.probe.windows(b.pid, b.existing)
		if err != nil {
			return nil, OutcomeUnavailable, fmt.Errorf("%w: %v", ErrPresentationUnavailable, err)
		}
		newWindows := make([]presentationWindow, 0, 2)
		for _, window := range windows {
			if window.ownerPID != b.pid || window.id == 0 {
				continue
			}
			if _, old := b.existing[window.id]; old {
				continue
			}
			newWindows = append(newWindows, window)
		}
		if len(newWindows) > 1 {
			releasePresentationWindows(b.probe, newWindows)
			return nil, OutcomeAmbiguous, ErrPresentationAmbiguous
		}
		if len(newWindows) == 1 {
			window := newWindows[0]
			eligible := window.onScreen && window.layer == 3 && window.role == "AXWindow" && window.subrole == "AXSystemDialog" &&
				window.frame.size == (presentationSize{width: 118, height: 32}) &&
				window.axMatchCount == 1 && window.recordingBarChildren && window.element != 0 && window.positionSettable &&
				samePoint(window.position, window.frame.origin) && sameSize(window.size, window.frame.size)
			if !eligible {
				releasePresentationWindows(b.probe, newWindows)
				candidateID, stableSince = 0, time.Time{}
			} else if candidateID != window.id {
				releasePresentationWindows(b.probe, newWindows)
				candidateID, stableSince = window.id, time.Now()
			} else if time.Since(stableSince) >= stableInterval {
				result, outcome, err := b.moveWindow(ctx, window)
				releasePresentationWindows(b.probe, newWindows)
				return result, outcome, err
			} else {
				releasePresentationWindows(b.probe, newWindows)
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

func (b *RecordingBarBaseline) moveWindow(ctx context.Context, window presentationWindow) (*Suppression, Outcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, OutcomeCancelled, err
	}
	if err := b.checkIdentity(); err != nil {
		return nil, OutcomeUnavailable, fmt.Errorf("%w: %v", ErrPresentationIdentityChanged, err)
	}
	current, found, err := b.probe.windowByID(b.pid, window.id)
	if err != nil || !found || current.ownerPID != b.pid || !current.onScreen ||
		current.layer != window.layer || !sameRect(current.frame, window.frame) {
		if err == nil {
			err = ErrPresentationWindowChanged
		}
		return nil, OutcomeUnchanged, fmt.Errorf("%w: %v", ErrPresentationWindowChanged, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, OutcomeCancelled, err
	}
	if err := b.checkIdentity(); err != nil {
		return nil, OutcomeUnavailable, fmt.Errorf("%w: %v", ErrPresentationIdentityChanged, err)
	}
	position, err := b.probe.position(window.element)
	if err != nil || !samePoint(position, window.position) {
		if err == nil {
			err = ErrPresentationWindowChanged
		}
		return nil, OutcomeUnchanged, fmt.Errorf("%w: %v", ErrPresentationWindowChanged, err)
	}
	pid, err := b.probe.elementPID(window.element)
	if err != nil || pid != b.pid {
		if err == nil {
			err = ErrPresentationWindowChanged
		}
		return nil, OutcomeUnchanged, fmt.Errorf("%w: %v", ErrPresentationWindowChanged, err)
	}
	if !finitePoint(position) {
		return nil, OutcomeUnchanged, ErrPresentationWindowChanged
	}
	parked := presentationPoint{x: -1000000, y: -1000000}
	if samePoint(position, parked) {
		return nil, OutcomeUnchanged, ErrPresentationWindowChanged
	}
	retained := b.probe.retain(window.element)
	if retained == 0 {
		return nil, OutcomeUnavailable, ErrPresentationUnavailable
	}
	suppression := &Suppression{
		pid: b.pid, windowID: window.id, checkIdentity: b.checkIdentity, probe: b.probe,
		element: retained, original: position, parked: parked,
	}
	if err := ctx.Err(); err != nil {
		_ = suppression.release()
		return nil, OutcomeCancelled, err
	}
	if err := b.probe.setPosition(window.element, parked); err != nil {
		// Return the lease even on error: the host must still try conditional cleanup.
		return suppression, OutcomeUnchanged, fmt.Errorf("doubao presentation: AXPosition set failed: %w", err)
	}
	confirmed, err := b.probe.position(window.element)
	if err != nil || !samePoint(confirmed, parked) {
		return suppression, OutcomeUnchanged, errors.New("doubao presentation: AXPosition write was not confirmed")
	}
	return suppression, OutcomeSuppressed, nil
}

func releasePresentationWindows(probe presentationProbe, windows []presentationWindow) {
	for _, window := range windows {
		if window.element != 0 {
			probe.release(window.element)
		}
	}
}

// Suppression owns one retained AX window element. Restore is conditional and
// idempotent: it writes the original position only while this same CGWindowID
// still belongs to the captured process and both CG and AX still report the
// exact position MiMac assigned. A user move or process/window replacement is
// left untouched. Cleanup runs even after the session context is cancelled.
type Suppression struct {
	mu            sync.Mutex
	pid           int32
	windowID      uint32
	checkIdentity func() error
	probe         presentationProbe
	element       uintptr
	original      presentationPoint
	parked        presentationPoint
	released      bool
	restored      bool
}

// RestoreAfterDismissal waits until the tool has ordered its window out.
// Moving a still-visible window home at key-up flashes its final animation.
// The owner cancels this wait when the user explicitly requests the stock UI.
func (s *Suppression) RestoreAfterDismissal(ctx context.Context) error {
	if s == nil {
		return nil
	}
	interval := 40 * time.Millisecond
	for {
		if err := s.checkIdentity(); err != nil {
			return s.release()
		}
		w, found, err := s.probe.windowByID(s.pid, s.windowID)
		if err != nil {
			_ = s.release()
			return err
		}
		if !found {
			return s.release()
		}
		if !w.onScreen {
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

func (s *Suppression) Restore(ctx context.Context) error {
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
		return ErrPresentationUnavailable
	}
	defer s.releaseLocked()
	if err := s.checkIdentity(); err != nil {
		return fmt.Errorf("%w: %v", ErrPresentationIdentityChanged, err)
	}
	window, found, err := s.probe.windowByID(s.pid, s.windowID)
	if err != nil || !found || window.ownerPID != s.pid {
		if err != nil {
			return fmt.Errorf("%w: %v", ErrPresentationWindowChanged, err)
		}
		return ErrPresentationWindowChanged
	}
	if !samePoint(window.frame.origin, s.parked) {
		return ErrPresentationMovedByUser
	}
	pid, err := s.probe.elementPID(s.element)
	if err != nil || pid != s.pid {
		if err != nil {
			return fmt.Errorf("%w: %v", ErrPresentationWindowChanged, err)
		}
		return ErrPresentationWindowChanged
	}
	position, err := s.probe.position(s.element)
	if err != nil || !samePoint(position, s.parked) {
		if err != nil {
			return fmt.Errorf("%w: %v", ErrPresentationWindowChanged, err)
		}
		return ErrPresentationMovedByUser
	}
	if err := s.checkIdentity(); err != nil {
		return fmt.Errorf("%w: %v", ErrPresentationIdentityChanged, err)
	}
	if err := s.probe.setPosition(s.element, s.original); err != nil {
		return fmt.Errorf("doubao presentation: AXPosition restore failed: %w", err)
	}
	restored, err := s.probe.position(s.element)
	if err != nil || !samePoint(restored, s.original) {
		return errors.New("doubao presentation: AXPosition restore was not confirmed")
	}
	s.restored = true
	return nil
}

func (s *Suppression) release() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.releaseLocked()
}

func (s *Suppression) releaseLocked() error {
	if s.released {
		return nil
	}
	s.probe.release(s.element)
	s.element = 0
	s.released = true
	return nil
}

func samePoint(a, b presentationPoint) bool {
	return a.x == b.x && a.y == b.y
}

func sameSize(a, b presentationSize) bool {
	return a.width == b.width && a.height == b.height
}

func sameRect(a, b presentationRect) bool {
	return samePoint(a.origin, b.origin) && sameSize(a.size, b.size)
}

func finitePoint(value presentationPoint) bool {
	return !math.IsNaN(value.x) && !math.IsNaN(value.y) && !math.IsInf(value.x, 0) && !math.IsInf(value.y, 0)
}
