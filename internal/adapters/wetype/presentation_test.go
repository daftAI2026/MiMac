package wetype

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/windowlease"
)

func TestWeTypeSuppressesOnlyTheSessionNewVoiceWindowAndRestoresIt(t *testing.T) {
	const pid = int32(4162)
	probe := newRecordingProbe()
	negative := testWeTypeWindow(34554, pid, 510, 32, true, []string{"AXButton", "AXImage", "AXStaticText"})
	negative.Position = windowlease.Point{X: 676, Y: 321}
	negative.Frame.Origin = negative.Position
	probe.windows[34554] = negative
	identityValid := true
	identityCheck := func() error {
		if !identityValid {
			return errors.New("process identity changed")
		}
		return nil
	}
	baseline, err := windowlease.CaptureBaseline(pid, identityCheck, probe)
	if err != nil {
		t.Fatal(err)
	}
	probe.windows[52574] = testWeTypeWindow(52574, pid, 200, 130, true, nil)
	wrapped := &RecordingBarBaseline{inner: baseline}
	lease, outcome, err := wrapped.Suppress(context.Background())
	if err != nil || outcome != windowlease.OutcomeSuppressed || lease == nil {
		t.Fatalf("Suppress() = (lease=%v, outcome=%q, err=%v), want a suppressed lease", lease != nil, outcome, err)
	}
	if got := probe.windows[52574].Position; got != (windowlease.Point{X: -1000000, Y: -1000000}) {
		t.Fatalf("parked position = %+v, want the shared offscreen position", got)
	}
	if probe.windows[34554].Position != (windowlease.Point{X: 676, Y: 321}) {
		t.Fatalf("preexisting candidate moved to %+v", probe.windows[34554].Position)
	}
	if err := lease.Restore(context.Background()); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if got := probe.windows[52574].Position; got != (windowlease.Point{X: 860, Y: 980}) {
		t.Fatalf("restored position = %+v, want original position", got)
	}
}

func TestWeTypeClassifierRequiresExactObservedMetadata(t *testing.T) {
	base := testWeTypeWindow(52574, 4162, 200, 130, true, nil)
	tests := []struct {
		name   string
		mutate func(*windowlease.Window)
	}{
		{name: "known empty shape", mutate: func(*windowlease.Window) {}},
		{name: "empty children not established", mutate: func(w *windowlease.Window) { w.ChildrenKnown = false }},
		{name: "nonempty child list", mutate: func(w *windowlease.Window) { w.ChildRoles = []string{"AXStaticText"} }},
		{name: "identifier unavailable", mutate: func(w *windowlease.Window) { w.IdentifierKnown = false }},
		{name: "identifier present", mutate: func(w *windowlease.Window) { w.IdentifierEmpty = false }},
		{name: "description unavailable", mutate: func(w *windowlease.Window) { w.DescriptionKnown = false }},
		{name: "description present", mutate: func(w *windowlease.Window) { w.DescriptionEmpty = false }},
		{name: "different size", mutate: func(w *windowlease.Window) { w.Frame.Size.Width = 201 }},
		{name: "different layer", mutate: func(w *windowlease.Window) { w.Layer++ }},
		{name: "different role", mutate: func(w *windowlease.Window) { w.Role = "AXGroup" }},
		{name: "ambiguous AX match", mutate: func(w *windowlease.Window) { w.AXMatchCount = 2 }},
		{name: "position not writable", mutate: func(w *windowlease.Window) { w.PositionSettable = false }},
		{name: "CG and AX position differ", mutate: func(w *windowlease.Window) { w.Position.X++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			window := base
			window.ChildRoles = append([]string(nil), base.ChildRoles...)
			test.mutate(&window)
			eligible := wechatRecordingWindow(window)
			want := test.name == "known empty shape"
			if eligible != want {
				t.Fatalf("wechatRecordingWindow() = %v, want %v", eligible, want)
			}
		})
	}
}

func TestWeTypeIdentityChangePreventsAnyWindowWrite(t *testing.T) {
	const pid = int32(4162)
	probe := newRecordingProbe()
	identityValid := true
	check := func() error {
		if !identityValid {
			return errors.New("process identity changed")
		}
		return nil
	}
	baseline, err := windowlease.CaptureBaseline(pid, check, probe)
	if err != nil {
		t.Fatal(err)
	}
	probe.windows[52574] = testWeTypeWindow(52574, pid, 200, 130, true, nil)
	identityValid = false
	_, outcome, err := (&RecordingBarBaseline{inner: baseline}).Suppress(context.Background())
	if outcome != windowlease.OutcomeUnavailable || !errors.Is(err, windowlease.ErrIdentityChanged) {
		t.Fatalf("Suppress() = (%q, %v), want identity failure", outcome, err)
	}
	if probe.writes != 0 {
		t.Fatalf("AX position writes = %d, want zero", probe.writes)
	}
}

func TestWeTypeMovedEventReparksOnceAndDestroyedEventRetiresLease(t *testing.T) {
	const pid = int32(4162)
	probe := newRecordingProbe()
	baseline, err := windowlease.CaptureBaseline(pid, func() error { return nil }, probe)
	if err != nil {
		t.Fatal(err)
	}
	probe.windows[52574] = testWeTypeWindow(52574, pid, 200, 130, true, nil)
	probe.writeSignal = make(chan int, 2)
	wrapped := &RecordingBarBaseline{inner: baseline}
	lease, outcome, err := wrapped.Suppress(context.Background())
	if err != nil || outcome != windowlease.OutcomeSuppressed || lease == nil || probe.events == nil {
		t.Fatalf("Suppress() = (lease=%v, outcome=%q, events=%v, err=%v), want a ready observed lease", lease != nil, outcome, probe.events != nil, err)
	}

	// AX reports the exact original point before CG updates its frame origin.
	window := probe.windows[52574]
	window.Position = windowlease.Point{X: 860, Y: 980}
	probe.windows[52574] = window
	queriesBeforeEnd := probe.windowQueries
	driverDone := make(chan struct{})
	go func() {
		probe.events.events <- windowlease.WindowEventMoved
		<-probe.writeSignal // initial park
		<-probe.writeSignal // event repark
		probe.events.events <- windowlease.WindowEventDestroyed
		close(driverDone)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lease.RestoreAfterDismissal(ctx); err != nil {
		t.Fatalf("normal dismissal cleanup error = %v", err)
	}
	<-driverDone
	if queries := probe.windowQueries - queriesBeforeEnd; queries != 1 {
		t.Fatalf("window queries for a move followed by destruction = %d, want 1", queries)
	}
	if got := probe.windows[52574].Position; got != (windowlease.Point{X: -1000000, Y: -1000000}) {
		t.Fatalf("repark position = %+v, want parked", got)
	}
	if probe.writes != 2 {
		t.Fatalf("AX writes = %d, want initial park and one repark only", probe.writes)
	}
}

func TestWeTypeReparksWhenAXReturnsBeforeCGGeometryUpdates(t *testing.T) {
	const pid = int32(4162)
	probe := newRecordingProbe()
	baseline, err := windowlease.CaptureBaseline(pid, func() error { return nil }, probe)
	if err != nil {
		t.Fatal(err)
	}
	probe.windows[52574] = testWeTypeWindow(52574, pid, 200, 130, true, nil)
	lease, outcome, err := (&RecordingBarBaseline{inner: baseline}).Suppress(context.Background())
	if err != nil || outcome != windowlease.OutcomeSuppressed || lease == nil {
		t.Fatalf("Suppress() = (%v, %q, %v), want a suppressed lease", lease != nil, outcome, err)
	}

	// Model WeType moving its own AX element to the exact pre-session origin
	// while CG still reports the one-frame-old parked position.
	window := probe.windows[52574]
	window.Position = windowlease.Point{X: 860, Y: 980}
	probe.windows[52574] = window
	observed, err := lease.ObserveWindow()
	if err != nil || !observed.Found || !observed.AtParked || !observed.SameGeometry {
		t.Fatalf("ObserveWindow() = (%+v, %v), want CG still parked with same geometry", observed, err)
	}
	reparked, err := lease.ReparkIfReturnedToOriginal()
	if err != nil || !reparked {
		t.Fatalf("ReparkIfReturnedToOriginal() = (%v, %v), want a confirmed repark", reparked, err)
	}
	if probe.writes != 2 || probe.windows[52574].Position != (windowlease.Point{X: -1000000, Y: -1000000}) {
		t.Fatalf("post-repark writes/position = %d/%+v, want two writes and parked position", probe.writes, probe.windows[52574].Position)
	}
	window = probe.windows[52574]
	window.OnScreen = false
	probe.windows[52574] = window
	if err := lease.RestoreAfterDismissal(context.Background()); err != nil {
		t.Fatalf("RestoreAfterDismissal() after orderOut error = %v", err)
	}
	if got := probe.windows[52574].Position; got != (windowlease.Point{X: 860, Y: 980}) {
		t.Fatalf("post-dismissal position = %+v, want original point", got)
	}
}

func TestWeTypeReparkLeavesUserMoveAndChangedGeometryAlone(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*windowlease.Window)
	}{
		{name: "user moved elsewhere", mutate: func(w *windowlease.Window) {
			w.Position = windowlease.Point{X: 100, Y: 200}
			w.Frame.Origin = w.Position
		}},
		{name: "geometry changed after return", mutate: func(w *windowlease.Window) {
			w.Position = windowlease.Point{X: 860, Y: 980}
			w.Frame.Origin = w.Position
			w.Frame.Size.Width++
			w.Size.Width++
		}},
		{name: "owner changed", mutate: func(w *windowlease.Window) {
			w.Position = windowlease.Point{X: 860, Y: 980}
			w.Frame.Origin = w.Position
			w.OwnerPID++
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			const pid = int32(4162)
			probe := newRecordingProbe()
			baseline, err := windowlease.CaptureBaseline(pid, func() error { return nil }, probe)
			if err != nil {
				t.Fatal(err)
			}
			probe.windows[52574] = testWeTypeWindow(52574, pid, 200, 130, true, nil)
			lease, _, err := (&RecordingBarBaseline{inner: baseline}).Suppress(context.Background())
			if err != nil || lease == nil {
				t.Fatalf("Suppress() = (%v, %v), want a lease", lease != nil, err)
			}
			window := probe.windows[52574]
			test.mutate(&window)
			probe.windows[52574] = window
			observed, err := lease.ObserveWindow()
			if err != nil {
				t.Fatalf("ObserveWindow() error = %v", err)
			}
			reparked := false
			if observed.Found {
				reparked, err = lease.ReparkIfReturnedToOriginal()
			}
			if reparked || (test.name != "owner changed" && err != nil) {
				t.Fatalf("ReparkIfReturnedToOriginal() = (%v, %v), want no write", reparked, err)
			}
			if probe.writes != 1 {
				t.Fatalf("AX writes = %d, want only initial park", probe.writes)
			}
			_ = lease.ReleaseWithoutRestore()
		})
	}
}

func TestWeTypeNextSessionCanCaptureReusedWindowNumberAndExplicitRestore(t *testing.T) {
	const pid = int32(4162)
	probe := newRecordingProbe()
	identity := func() error { return nil }
	firstBaseline, err := windowlease.CaptureBaseline(pid, identity, probe)
	if err != nil {
		t.Fatal(err)
	}
	probe.windows[52574] = testWeTypeWindow(52574, pid, 200, 130, true, nil)
	firstLease, _, err := (&RecordingBarBaseline{inner: firstBaseline}).Suppress(context.Background())
	if err != nil || firstLease == nil {
		t.Fatalf("first Suppress() = (%v, %v), want lease", firstLease != nil, err)
	}
	window := probe.windows[52574]
	window.OnScreen = false
	probe.windows[52574] = window
	if err := firstLease.WaitForOrderOut(context.Background()); err != nil {
		t.Fatalf("first session cleanup error = %v", err)
	}

	// The old window is no longer in the visible baseline. A later session may
	// legitimately receive the same CGWindowID and must get an independent lease.
	secondBaseline, err := windowlease.CaptureBaseline(pid, identity, probe)
	if err != nil {
		t.Fatal(err)
	}
	window = testWeTypeWindow(52574, pid, 200, 130, true, nil)
	probe.windows[52574] = window
	secondLease, outcome, err := (&RecordingBarBaseline{inner: secondBaseline}).Suppress(context.Background())
	if err != nil || outcome != windowlease.OutcomeSuppressed || secondLease == nil {
		t.Fatalf("second Suppress() = (%v, %q, %v), want a fresh lease", secondLease != nil, outcome, err)
	}
	if err := secondLease.Restore(context.Background()); err != nil {
		t.Fatalf("explicit restore error = %v", err)
	}
	if got := probe.windows[52574].Position; got != (windowlease.Point{X: 860, Y: 980}) {
		t.Fatalf("explicit restore position = %+v, want original point", got)
	}
}

func testWeTypeWindow(id uint32, pid int32, width, height float64, childrenKnown bool, childRoles []string) windowlease.Window {
	frame := windowlease.Rect{Origin: windowlease.Point{X: 860, Y: 980}, Size: windowlease.Size{Width: width, Height: height}}
	return windowlease.Window{
		ID: id, OwnerPID: pid, Layer: wechatRecordingLayer, Frame: frame, OnScreen: true,
		Role: "AXWindow", Subrole: "AXSystemDialog", IdentifierKnown: true, IdentifierEmpty: true,
		DescriptionKnown: true, DescriptionEmpty: true, Element: uintptr(id), Position: frame.Origin,
		Size: frame.Size, AXMatchCount: 1, PositionSettable: true,
		ChildrenKnown: childrenKnown, ChildRoles: childRoles,
	}
}

type recordingProbe struct {
	windowQueries int
	windows       map[uint32]windowlease.Window
	writes        int
	events        *recordingEventSource
	writeSignal   chan int
}

func newRecordingProbe() *recordingProbe {
	return &recordingProbe{windows: make(map[uint32]windowlease.Window)}
}

func (p *recordingProbe) WindowNumbers(pid int32) (map[uint32]struct{}, error) {
	ids := make(map[uint32]struct{})
	for id, window := range p.windows {
		if window.OwnerPID == pid && window.OnScreen {
			ids[id] = struct{}{}
		}
	}
	return ids, nil
}

func (p *recordingProbe) Windows(pid int32, newerThan map[uint32]struct{}) ([]windowlease.Window, error) {
	var result []windowlease.Window
	for id, window := range p.windows {
		if window.OwnerPID != pid || !window.OnScreen {
			continue
		}
		if _, exists := newerThan[id]; !exists {
			result = append(result, window)
		}
	}
	return result, nil
}

func (p *recordingProbe) WindowByID(pid int32, id uint32) (windowlease.Window, bool, error) {
	p.windowQueries++
	window, ok := p.windows[id]
	return window, ok && window.OwnerPID == pid, nil
}

func (p *recordingProbe) SetPosition(element uintptr, point windowlease.Point) error {
	p.writes++
	for id, window := range p.windows {
		if window.Element == element {
			window.Position = point
			window.Frame.Origin = point
			p.windows[id] = window
			if p.writeSignal != nil {
				p.writeSignal <- p.writes
			}
			return nil
		}
	}
	return windowlease.ErrWindowChanged
}

func (p *recordingProbe) Position(element uintptr) (windowlease.Point, error) {
	for _, window := range p.windows {
		if window.Element == element {
			return window.Position, nil
		}
	}
	return windowlease.Point{}, windowlease.ErrWindowChanged
}

func (p *recordingProbe) Size(element uintptr) (windowlease.Size, error) {
	for _, window := range p.windows {
		if window.Element == element {
			return window.Size, nil
		}
	}
	return windowlease.Size{}, windowlease.ErrWindowChanged
}

func (p *recordingProbe) ElementPID(element uintptr) (int32, error) {
	for _, window := range p.windows {
		if window.Element == element {
			return window.OwnerPID, nil
		}
	}
	return 0, windowlease.ErrWindowChanged
}

func (p *recordingProbe) Retain(element uintptr) uintptr { return element }
func (p *recordingProbe) Release(uintptr)                {}

func (p *recordingProbe) WatchWindow(pid int32, element uintptr) (windowlease.WindowEventSource, error) {
	window, ok := p.windows[52574]
	if !ok || window.OwnerPID != pid || window.Element != element {
		return nil, windowlease.ErrWindowChanged
	}
	p.events = &recordingEventSource{events: make(chan windowlease.WindowEvent, 8)}
	return p.events, nil
}

type recordingEventSource struct {
	events chan windowlease.WindowEvent
	once   sync.Once
}

func (s *recordingEventSource) Events() <-chan windowlease.WindowEvent { return s.events }
func (s *recordingEventSource) Close() {
	s.once.Do(func() { close(s.events) })
}

var _ windowlease.Probe = (*recordingProbe)(nil)
