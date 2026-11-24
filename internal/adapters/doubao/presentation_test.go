package doubao

import (
	"context"
	"errors"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/windowlease"
)

// The fake probe and its legacy-shaped records stay in this test file. Product
// code now operates directly on the shared windowlease contract.
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

func captureRecordingBarBaseline(pid int32, checkIdentity func() error, probe presentationProbe) (*RecordingBarBaseline, error) {
	return captureRecordingBarBaselineWithProbe(pid, checkIdentity, testPresentationProbeAdapter{probe})
}

func suppressDoubaoForTest(baseline *RecordingBarBaseline, ctx context.Context, observeLimit, pollInterval, stableInterval time.Duration) (*Suppression, Outcome, error) {
	if baseline == nil || baseline.inner == nil {
		return nil, OutcomeUnavailable, ErrPresentationUnavailable
	}
	return baseline.inner.Suppress(ctx, doubaoRecordingWindow, observeLimit, pollInterval, stableInterval)
}

func TestRecordingBarPresentationSuppressesOnlyOneNewSemanticWindow(t *testing.T) {
	const pid = int32(42)
	probe := newFakePresentationProbe(
		presentationWindow{id: 10, ownerPID: pid, layer: 3, onScreen: true},
	)
	var identityValid = true
	checkIdentity := func() error {
		if !identityValid {
			return errors.New("changed")
		}
		return nil
	}
	baseline, err := captureRecordingBarBaseline(pid, checkIdentity, probe)
	if err != nil {
		t.Fatal(err)
	}

	frame := presentationRect{
		origin: presentationPoint{x: 1420, y: -380},
		size:   presentationSize{width: 118, height: 32},
	}
	probe.add(presentationWindow{
		id: 11, ownerPID: pid, layer: 3, frame: frame, onScreen: true,
		role: "AXWindow", subrole: "AXSystemDialog", element: 700,
		position: frame.origin, size: frame.size, axMatchCount: 1,
		positionSettable: true, recordingBarChildren: true,
	})

	suppression, outcome, err := suppressDoubaoForTest(baseline, context.Background(), time.Second, time.Millisecond, 0)
	if err != nil || outcome != OutcomeSuppressed || suppression == nil {
		t.Fatalf("Suppress() = (%v, %q, %v), want a lease and suppression", suppression != nil, outcome, err)
	}
	parked := presentationPoint{x: -1000000, y: -1000000}
	if got := probe.positionOf(700); got != parked {
		t.Fatalf("parked position = %+v, want %+v", got, parked)
	}
	if err := suppression.Restore(context.Background()); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if got := probe.positionOf(700); got != frame.origin {
		t.Fatalf("restored position = %+v, want %+v", got, frame.origin)
	}
	if got := probe.counts(); got.retained != 1 || got.released != 3 {
		t.Fatalf("AX element ownership = %+v, want one lease retain and three releases", got)
	}
}

func TestRecordingBarPresentationExcludesExistingAndAmbiguousWindows(t *testing.T) {
	const pid = int32(42)
	tests := []struct {
		name       string
		before     []presentationWindow
		after      []presentationWindow
		want       Outcome
		wantWrites int
	}{
		{
			name:   "preexisting candidate remains unchanged",
			before: []presentationWindow{testRecordingWindow(10, pid, presentationPoint{x: 2, y: 3}, true)},
			want:   OutcomeUnchanged,
		},
		{
			name: "two new windows remain unchanged",
			after: []presentationWindow{
				testRecordingWindow(11, pid, presentationPoint{x: 12, y: 13}, true),
				testRecordingWindow(12, pid, presentationPoint{x: 30, y: 40}, true),
			},
			want: OutcomeAmbiguous,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe := newFakePresentationProbe(test.before...)
			baseline, err := captureRecordingBarBaseline(pid, func() error { return nil }, probe)
			if err != nil {
				t.Fatal(err)
			}
			for _, window := range test.after {
				probe.add(window)
			}
			_, outcome, err := suppressDoubaoForTest(baseline, context.Background(), time.Millisecond, time.Millisecond, 0)
			if !errors.Is(err, ErrPresentationAmbiguous) && test.want == OutcomeAmbiguous {
				t.Fatalf("Suppress() error = %v, want ambiguity", err)
			}
			if outcome != test.want {
				t.Fatalf("Suppress() outcome = %q, want %q", outcome, test.want)
			}
			if got := probe.counts().writes; got != test.wantWrites {
				t.Fatalf("AX position writes = %d, want %d", got, test.wantWrites)
			}
		})
	}
}

func TestRecordingBarPresentationRequiresVerifiedSemanticMarkerAndStructure(t *testing.T) {
	const pid = int32(42)
	tests := []struct {
		name   string
		mutate func(*presentationWindow)
	}{
		{name: "candidate layer", mutate: func(w *presentationWindow) { w.layer = 2147483628 }},
		{name: "different panel layout", mutate: func(w *presentationWindow) { w.frame.size.width = 300; w.size = w.frame.size }},
		{name: "different window subrole", mutate: func(w *presentationWindow) { w.subrole = "AXStandardWindow" }},
		{name: "no semantic marker", mutate: func(w *presentationWindow) { w.recordingBarChildren = false }},
		{name: "unique system dialog geometry is not a marker", mutate: func(w *presentationWindow) {
			w.subrole = "AXSystemDialog"
			w.recordingBarChildren = false
			w.size = presentationSize{width: 173, height: 39}
			w.frame.size = w.size
		}},
		{name: "wrong role", mutate: func(w *presentationWindow) { w.role = "AXList" }},
		{name: "wrong layer", mutate: func(w *presentationWindow) { w.layer = 0 }},
		{name: "ambiguous AX mapping", mutate: func(w *presentationWindow) { w.axMatchCount = 2 }},
		{name: "position not settable", mutate: func(w *presentationWindow) { w.positionSettable = false }},
		{name: "AX and CG frames differ", mutate: func(w *presentationWindow) { w.position.x++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			window := testRecordingWindow(11, pid, presentationPoint{x: 220, y: 140}, true)
			test.mutate(&window)
			probe := newFakePresentationProbe()
			baseline, err := captureRecordingBarBaseline(pid, func() error { return nil }, probe)
			if err != nil {
				t.Fatal(err)
			}
			probe.add(window)
			lease, outcome, err := suppressDoubaoForTest(baseline, context.Background(), time.Millisecond, time.Millisecond, 0)
			if err != nil || outcome != OutcomeUnchanged || lease != nil {
				t.Fatalf("Suppress() = (lease:%v, outcome:%q, err:%v), want unchanged", lease != nil, outcome, err)
			}
			if got := probe.counts().writes; got != 0 {
				t.Fatalf("AX position writes = %d, want 0", got)
			}
		})
	}
}

func TestRecordingBarPresentationRestoreHonorsMoveIdentityAndCancellation(t *testing.T) {
	const pid = int32(42)
	for _, test := range []struct {
		name       string
		prepare    func(*fakePresentationProbe, *bool, *Suppression)
		wantWrites int
		wantErr    error
	}{
		{
			name:       "session cancellation still restores",
			wantWrites: 2,
		},
		{
			name: "user move is preserved",
			prepare: func(probe *fakePresentationProbe, _ *bool, _ *Suppression) {
				probe.setExternalPosition(711, presentationPoint{x: 810, y: 820})
			},
			wantWrites: 1,
			wantErr:    ErrPresentationMovedByUser,
		},
		{
			name: "changed process identity is preserved",
			prepare: func(_ *fakePresentationProbe, identityValid *bool, _ *Suppression) {
				*identityValid = false
			},
			wantWrites: 1,
			wantErr:    ErrPresentationIdentityChanged,
		},
		{
			name: "closed window is not restored by number reuse",
			prepare: func(probe *fakePresentationProbe, _ *bool, _ *Suppression) {
				probe.remove(11)
				replacement := testRecordingWindow(11, pid, presentationPoint{x: -1000000, y: -1000000}, true)
				replacement.element = 999
				probe.add(replacement)
			},
			wantWrites: 1,
			wantErr:    ErrPresentationWindowChanged,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			probe := newFakePresentationProbe()
			identityValid := true
			baseline, err := captureRecordingBarBaseline(pid, func() error {
				if !identityValid {
					return errors.New("changed")
				}
				return nil
			}, probe)
			if err != nil {
				t.Fatal(err)
			}
			window := testRecordingWindow(11, pid, presentationPoint{x: 110, y: 120}, true)
			probe.add(window)
			lease, outcome, err := suppressDoubaoForTest(baseline, context.Background(), time.Second, time.Millisecond, 0)
			if err != nil || outcome != OutcomeSuppressed || lease == nil {
				t.Fatalf("Suppress() = (%v, %q, %v), want suppressed lease", lease != nil, outcome, err)
			}
			if test.prepare != nil {
				test.prepare(probe, &identityValid, lease)
			}
			cleanupCtx, cancel := context.WithCancel(context.Background())
			cancel()
			err = lease.Restore(cleanupCtx)
			if test.wantErr == nil && err != nil {
				t.Fatalf("Restore(cancelled ctx) error = %v, want nil", err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("Restore() error = %v, want %v", err, test.wantErr)
			}
			if got := probe.counts().writes; got != test.wantWrites {
				t.Fatalf("AX position writes = %d, want %d", got, test.wantWrites)
			}
		})
	}
}

func TestRecordingBarPresentationCancellationDoesNotMutateLateWindow(t *testing.T) {
	const pid = int32(42)
	probe := newFakePresentationProbe()
	baseline, err := captureRecordingBarBaseline(pid, func() error { return nil }, probe)
	if err != nil {
		t.Fatal(err)
	}
	window := testRecordingWindow(11, pid, presentationPoint{x: 10, y: 20}, true)
	probe.windowsHook = func() {
		probe.add(window)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lease, outcome, err := suppressDoubaoForTest(baseline, ctx, time.Second, time.Millisecond, 0)
	if !errors.Is(err, context.Canceled) || outcome != OutcomeCancelled || lease != nil {
		t.Fatalf("Suppress(cancelled ctx) = (%v, %q, %v), want cancelled and no lease", lease != nil, outcome, err)
	}
	if got := probe.counts().writes; got != 0 {
		t.Fatalf("late AX position writes = %d, want 0", got)
	}
}

func TestEndedSessionRestoresOnlyAfterWindowOrdersOut(t *testing.T) {
	const pid = int32(42)
	window := testRecordingWindow(12, pid, presentationPoint{x: 40, y: 60}, true)
	probe := newFakePresentationProbe()
	baseline, err := captureRecordingBarBaseline(pid, func() error { return nil }, probe)
	if err != nil {
		t.Fatal(err)
	}
	probe.add(window)
	lease, _, err := suppressDoubaoForTest(baseline, context.Background(), time.Second, time.Millisecond, 0)
	if err != nil || lease == nil {
		t.Fatal(err)
	}
	checkedWhileVisible := false
	count := 0
	probe.lookupHook = func() {
		count++
		if count == 1 {
			checkedWhileVisible = probe.positionOf(window.element) == presentationPoint{x: -1000000, y: -1000000}
			return
		}
		current := probe.records[window.id]
		current.onScreen = false
		probe.add(current)
	}
	if err = lease.RestoreAfterDismissal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !checkedWhileVisible || probe.positionOf(window.element) != window.position {
		t.Fatal("window flashed on release or remained parked after dismissal")
	}
}

func testRecordingWindow(id uint32, pid int32, origin presentationPoint, marker bool) presentationWindow {
	size := presentationSize{width: 118, height: 32}
	return presentationWindow{
		id: id, ownerPID: pid, layer: 3, onScreen: true,
		frame: presentationRect{origin: origin, size: size},
		role:  "AXWindow", subrole: "AXSystemDialog", element: uintptr(id) + 700,
		position: origin, size: size, axMatchCount: 1,
		positionSettable: true, recordingBarChildren: marker,
	}
}

type fakePresentationCounts struct {
	writes              int
	retained            int
	released            int
	windowNumberQueries int
}

type fakePresentationProbe struct {
	records     map[uint32]presentationWindow
	countsValue fakePresentationCounts
	windowsHook func()
	lookupHook  func()
}

func newFakePresentationProbe(windows ...presentationWindow) *fakePresentationProbe {
	probe := &fakePresentationProbe{records: make(map[uint32]presentationWindow)}
	for _, window := range windows {
		probe.add(window)
	}
	return probe
}

func (p *fakePresentationProbe) add(window presentationWindow) {
	p.records[window.id] = window
}

func (p *fakePresentationProbe) remove(id uint32) {
	delete(p.records, id)
}

func (p *fakePresentationProbe) setExternalPosition(element uintptr, position presentationPoint) {
	for id, window := range p.records {
		if window.element == element {
			window.position = position
			window.frame.origin = position
			p.records[id] = window
		}
	}
}

func (p *fakePresentationProbe) windowNumbers(pid int32) (map[uint32]struct{}, error) {
	p.countsValue.windowNumberQueries++
	result := make(map[uint32]struct{})
	for _, window := range p.records {
		if window.ownerPID == pid {
			result[window.id] = struct{}{}
		}
	}
	return result, nil
}

func (p *fakePresentationProbe) windows(pid int32, newerThan map[uint32]struct{}) ([]presentationWindow, error) {
	if p.windowsHook != nil {
		p.windowsHook()
		p.windowsHook = nil
	}
	var result []presentationWindow
	for _, window := range p.records {
		if window.ownerPID != pid {
			continue
		}
		if _, old := newerThan[window.id]; old {
			continue
		}
		result = append(result, window)
	}
	return result, nil
}

func (p *fakePresentationProbe) windowByID(pid int32, id uint32) (presentationWindow, bool, error) {
	if p.lookupHook != nil {
		p.lookupHook()
	}
	window, ok := p.records[id]
	return window, ok && window.ownerPID == pid, nil
}

func (p *fakePresentationProbe) setPosition(element uintptr, position presentationPoint) error {
	p.countsValue.writes++
	for id, window := range p.records {
		if window.element == element {
			window.position = position
			window.frame.origin = position
			p.records[id] = window
			return nil
		}
	}
	return ErrPresentationWindowChanged
}

func (p *fakePresentationProbe) position(element uintptr) (presentationPoint, error) {
	for _, window := range p.records {
		if window.element == element {
			return window.position, nil
		}
	}
	return presentationPoint{}, ErrPresentationWindowChanged
}

func (p *fakePresentationProbe) positionOf(element uintptr) presentationPoint {
	position, _ := p.position(element)
	return position
}

func (p *fakePresentationProbe) elementPID(element uintptr) (int32, error) {
	for _, window := range p.records {
		if window.element == element {
			return window.ownerPID, nil
		}
	}
	return 0, ErrPresentationWindowChanged
}

func (p *fakePresentationProbe) retain(element uintptr) uintptr {
	if element != 0 {
		p.countsValue.retained++
	}
	return element
}

func (p *fakePresentationProbe) release(element uintptr) {
	if element != 0 {
		p.countsValue.released++
	}
}

func (p *fakePresentationProbe) counts() fakePresentationCounts { return p.countsValue }

var _ presentationProbe = (*fakePresentationProbe)(nil)

type testPresentationProbeAdapter struct{ probe presentationProbe }

func (p testPresentationProbeAdapter) WindowNumbers(pid int32) (map[uint32]struct{}, error) {
	return p.probe.windowNumbers(pid)
}
func (p testPresentationProbeAdapter) Windows(pid int32, newerThan map[uint32]struct{}) ([]windowlease.Window, error) {
	windows, err := p.probe.windows(pid, newerThan)
	if err != nil {
		return nil, err
	}
	result := make([]windowlease.Window, 0, len(windows))
	for _, window := range windows {
		result = append(result, toSharedPresentationWindow(window))
	}
	return result, nil
}
func (p testPresentationProbeAdapter) WindowByID(pid int32, id uint32) (windowlease.Window, bool, error) {
	window, found, err := p.probe.windowByID(pid, id)
	return toSharedPresentationWindow(window), found, err
}
func (p testPresentationProbeAdapter) SetPosition(element uintptr, point windowlease.Point) error {
	return p.probe.setPosition(element, presentationPoint{x: point.X, y: point.Y})
}
func (p testPresentationProbeAdapter) Position(element uintptr) (windowlease.Point, error) {
	point, err := p.probe.position(element)
	return windowlease.Point{X: point.x, Y: point.y}, err
}
func (p testPresentationProbeAdapter) ElementPID(element uintptr) (int32, error) {
	return p.probe.elementPID(element)
}
func (p testPresentationProbeAdapter) Retain(element uintptr) uintptr { return p.probe.retain(element) }
func (p testPresentationProbeAdapter) Release(element uintptr)        { p.probe.release(element) }

func toSharedPresentationWindow(window presentationWindow) windowlease.Window {
	var children []string
	if window.recordingBarChildren {
		children = []string{"AXStaticText", "AXStaticText"}
	}
	return windowlease.Window{
		ID: window.id, OwnerPID: window.ownerPID, Layer: window.layer,
		Frame: windowlease.Rect{
			Origin: windowlease.Point{X: window.frame.origin.x, Y: window.frame.origin.y},
			Size:   windowlease.Size{Width: window.frame.size.width, Height: window.frame.size.height},
		},
		OnScreen: window.onScreen, Role: window.role, Subrole: window.subrole, Element: window.element,
		Position:     windowlease.Point{X: window.position.x, Y: window.position.y},
		Size:         windowlease.Size{Width: window.size.width, Height: window.size.height},
		AXMatchCount: window.axMatchCount, PositionSettable: window.positionSettable,
		ChildrenKnown: window.recordingBarChildren, ChildRoles: children,
	}
}

var _ windowlease.Probe = testPresentationProbeAdapter{}
