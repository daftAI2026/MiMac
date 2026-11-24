package wetype

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
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

func TestWeTypeReparksResizeDuringCaptureAndAfterKeyUp(t *testing.T) {
	const pid = int32(4162)
	probe := newRecordingProbe()
	baseline, err := windowlease.CaptureBaseline(pid, func() error { return nil }, probe)
	if err != nil {
		t.Fatal(err)
	}
	probe.setWindow(testWeTypeWindow(52574, pid, 200, 130, true, nil))
	probe.writeSignal = make(chan int, 4)
	probe.querySignal = make(chan struct{}, 8)
	lease, outcome, err := (&RecordingBarBaseline{inner: baseline}).Suppress(context.Background())
	if err != nil || outcome != windowlease.OutcomeSuppressed || lease == nil || probe.eventSource() == nil {
		t.Fatalf("Suppress() = (lease=%v, outcome=%q, events=%v, err=%v), want a lease with observer ready before park", lease != nil, outcome, probe.eventSource() != nil, err)
	}
	if got := <-probe.writeSignal; got != 1 {
		t.Fatalf("initial AX write = %d, want park", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ended := make(chan struct{})
	maintained := make(chan error, 1)
	go func() { maintained <- lease.Maintain(ctx, ended) }()

	// The known target reflows from 200 to 254 points, preserving center, Y,
	// and height. AX leads CG: the AX geometry is on screen while CG still has
	// the old parked 200-point frame. A native resize event must repark now.
	window := probe.window(52574)
	window.Position = windowlease.Point{X: 833, Y: 980}
	window.Size = windowlease.Size{Width: 254, Height: 130}
	probe.setWindow(window)
	probe.sendEvent(windowlease.WindowEventResized)
	waitProbeQuery(t, probe.querySignal)
	if got := waitWrite(t, probe.writeSignal); got != 2 {
		t.Fatalf("AX-leading resize writes = %d, want one repark after initial park", got)
	}

	// CG catches up while AX remains parked. This second resize event must not
	// duplicate the write.
	window = probe.window(52574)
	window.Frame.Origin = windowlease.Point{X: 833, Y: 980}
	window.Frame.Size = windowlease.Size{Width: 254, Height: 130}
	probe.setWindow(window)
	probe.sendEvent(windowlease.WindowEventResized)
	waitProbeQuery(t, probe.querySignal)

	// End capture, then model WeType's later return to the original 200-point
	// shape before its hide/destroy notification.
	close(ended)
	window = probe.window(52574)
	window.Position = windowlease.Point{X: 860, Y: 980}
	window.Size = windowlease.Size{Width: 200, Height: 130}
	window.Frame.Origin = window.Position
	window.Frame.Size = window.Size
	probe.setWindow(window)
	probe.sendEvent(windowlease.WindowEventMoved)
	waitProbeQuery(t, probe.querySignal)
	if got := waitWrite(t, probe.writeSignal); got != 3 {
		t.Fatalf("return-to-original writes = %d, want one repark after key-up", got)
	}
	probe.sendEvent(windowlease.WindowEventDestroyed)
	if err := waitMaintenance(t, maintained); err != nil {
		t.Fatalf("maintained lease returned error: %v", err)
	}
	if got := probe.writeCount(); got != 3 {
		t.Fatalf("AX writes = %d, want initial park plus two geometry re-parks", got)
	}
	if !lease.IsReleased() {
		t.Fatal("destroyed event did not retire the window lease")
	}
}

func TestWeTypeReparkUsesOneBoundSnapshotWithoutRepeatedIdentityChecks(t *testing.T) {
	const pid = int32(4162)
	probe := newRecordingProbe()
	var identityChecks atomic.Int32
	baseline, err := windowlease.CaptureBaseline(pid, func() error {
		identityChecks.Add(1)
		return nil
	}, probe)
	if err != nil {
		t.Fatal(err)
	}
	probe.setWindow(testWeTypeWindow(52574, pid, 200, 130, true, nil))
	probe.writeSignal = make(chan int, 2)
	probe.querySignal = make(chan struct{}, 2)
	lease, _, err := (&RecordingBarBaseline{inner: baseline}).Suppress(context.Background())
	if err != nil || lease == nil {
		t.Fatalf("Suppress() = (%v, %v), want lease", lease != nil, err)
	}
	if got := waitWrite(t, probe.writeSignal); got != 1 {
		t.Fatalf("initial AX write = %d, want park", got)
	}
	metricsBefore := probe.metrics()
	identityBefore := identityChecks.Load()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	maintained := make(chan error, 1)
	go func() { maintained <- lease.Maintain(ctx, nil) }()

	window := probe.window(52574)
	window.Position = windowlease.Point{X: 833, Y: 980}
	window.Size = windowlease.Size{Width: 254, Height: 130}
	probe.setWindow(window)
	probe.sendEvent(windowlease.WindowEventResized)
	waitProbeQuery(t, probe.querySignal)
	if got := waitWrite(t, probe.writeSignal); got != 2 {
		t.Fatalf("repark AX write = %d, want one repark", got)
	}
	probe.sendEvent(windowlease.WindowEventDestroyed)
	if err := waitMaintenance(t, maintained); err != nil {
		t.Fatalf("maintained lease returned error: %v", err)
	}
	metricsAfter := probe.metrics()
	if got := metricsAfter.boundSnapshots - metricsBefore.boundSnapshots; got != 1 {
		t.Fatalf("bound AX/CG snapshots = %d, want one", got)
	}
	if got := metricsAfter.elementPIDReads - metricsBefore.elementPIDReads; got != 1 {
		t.Fatalf("retained-element PID reads = %d, want one", got)
	}
	if got := metricsAfter.positionReads - metricsBefore.positionReads; got != 2 {
		t.Fatalf("AXPosition reads = %d, want one snapshot plus write confirmation", got)
	}
	if got := metricsAfter.sizeReads - metricsBefore.sizeReads; got != 1 {
		t.Fatalf("AXSize reads = %d, want one snapshot", got)
	}
	if got := identityChecks.Load() - identityBefore; got != 0 {
		t.Fatalf("process-file identity checks in repark hot path = %d, want zero", got)
	}
}

func TestWeTypeDoesNotReparkUserMoveAndDoesNotReenableLater(t *testing.T) {
	const pid = int32(4162)
	probe := newRecordingProbe()
	baseline, err := windowlease.CaptureBaseline(pid, func() error { return nil }, probe)
	if err != nil {
		t.Fatal(err)
	}
	probe.setWindow(testWeTypeWindow(52574, pid, 200, 130, true, nil))
	lease, _, err := (&RecordingBarBaseline{inner: baseline}).Suppress(context.Background())
	if err != nil || lease == nil {
		t.Fatalf("Suppress() = (%v, %v), want lease", lease != nil, err)
	}
	probe.querySignal = make(chan struct{}, 4)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	maintained := make(chan error, 1)
	go func() { maintained <- lease.Maintain(ctx, nil) }()

	window := probe.window(52574)
	window.Position = windowlease.Point{X: 100, Y: 200}
	window.Frame.Origin = window.Position
	probe.setWindow(window)
	probe.sendEvent(windowlease.WindowEventMoved)
	waitProbeQuery(t, probe.querySignal)

	// Even if the same element later returns to an approved center, the first
	// unrelated geometry disabled this lease's repark policy.
	window = probe.window(52574)
	window.Position = windowlease.Point{X: 860, Y: 980}
	window.Frame.Origin = window.Position
	probe.setWindow(window)
	probe.sendEvent(windowlease.WindowEventMoved)
	probe.sendEvent(windowlease.WindowEventDestroyed)
	if err := waitMaintenance(t, maintained); err != nil {
		t.Fatalf("maintained lease returned error: %v", err)
	}
	if got := probe.writeCount(); got != 1 {
		t.Fatalf("AX writes = %d, want only the initial park after an unrecognized move", got)
	}
}

func TestWeTypeCancelledMaintenanceRestoresAndRetiresObserver(t *testing.T) {
	const pid = int32(4162)
	probe := newRecordingProbe()
	baseline, err := windowlease.CaptureBaseline(pid, func() error { return nil }, probe)
	if err != nil {
		t.Fatal(err)
	}
	probe.setWindow(testWeTypeWindow(52574, pid, 200, 130, true, nil))
	lease, _, err := (&RecordingBarBaseline{inner: baseline}).Suppress(context.Background())
	if err != nil || lease == nil {
		t.Fatalf("Suppress() = (%v, %v), want lease", lease != nil, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	maintained := make(chan error, 1)
	go func() { maintained <- lease.Maintain(ctx, make(chan struct{})) }()
	cancel()
	if err := waitMaintenance(t, maintained); err != nil {
		t.Fatalf("cancel cleanup error = %v", err)
	}
	if got := probe.window(52574).Position; got != (windowlease.Point{X: 860, Y: 980}) {
		t.Fatalf("cancel restored position = %+v, want original", got)
	}
	if !lease.IsReleased() {
		t.Fatal("cancel did not retire the event source")
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
	mu              sync.Mutex
	windows         map[uint32]windowlease.Window
	writes          int
	boundSnapshots  int
	elementPIDReads int
	positionReads   int
	sizeReads       int
	events          *recordingEventSource
	writeSignal     chan int
	querySignal     chan struct{}
}

func newRecordingProbe() *recordingProbe {
	return &recordingProbe{windows: make(map[uint32]windowlease.Window)}
}

func (p *recordingProbe) WindowNumbers(pid int32) (map[uint32]struct{}, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := make(map[uint32]struct{})
	for id, window := range p.windows {
		if window.OwnerPID == pid && window.OnScreen {
			ids[id] = struct{}{}
		}
	}
	return ids, nil
}

func (p *recordingProbe) Windows(pid int32, newerThan map[uint32]struct{}) ([]windowlease.Window, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
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
	p.mu.Lock()
	defer p.mu.Unlock()
	window, ok := p.windows[id]
	return window, ok && window.OwnerPID == pid, nil
}

func (p *recordingProbe) WindowByIDWithAX(pid int32, id uint32, element uintptr) (windowlease.Window, bool, error) {
	p.mu.Lock()
	p.boundSnapshots++
	p.elementPIDReads++
	p.positionReads++
	p.sizeReads++
	window, ok := p.windows[id]
	querySignal := p.querySignal
	p.mu.Unlock()
	if querySignal != nil {
		querySignal <- struct{}{}
	}
	return window, ok && window.OwnerPID == pid && window.Element == element, nil
}

func (p *recordingProbe) SetPosition(element uintptr, point windowlease.Point) error {
	p.mu.Lock()
	p.writes++
	writes := p.writes
	writeSignal := p.writeSignal
	for id, window := range p.windows {
		if window.Element == element {
			window.Position = point
			window.Frame.Origin = point
			p.windows[id] = window
			p.mu.Unlock()
			if writeSignal != nil {
				writeSignal <- writes
			}
			return nil
		}
	}
	p.mu.Unlock()
	return windowlease.ErrWindowChanged
}

func (p *recordingProbe) Position(element uintptr) (windowlease.Point, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.positionReads++
	for _, window := range p.windows {
		if window.Element == element {
			return window.Position, nil
		}
	}
	return windowlease.Point{}, windowlease.ErrWindowChanged
}

func (p *recordingProbe) Size(element uintptr) (windowlease.Size, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sizeReads++
	for _, window := range p.windows {
		if window.Element == element {
			return window.Size, nil
		}
	}
	return windowlease.Size{}, windowlease.ErrWindowChanged
}

func (p *recordingProbe) ElementPID(element uintptr) (int32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.elementPIDReads++
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
	p.mu.Lock()
	defer p.mu.Unlock()
	window, ok := p.windows[52574]
	if !ok || window.OwnerPID != pid || window.Element != element {
		return nil, windowlease.ErrWindowChanged
	}
	p.events = &recordingEventSource{events: make(chan windowlease.WindowEvent, 8)}
	return p.events, nil
}

func (p *recordingProbe) setWindow(window windowlease.Window) {
	p.mu.Lock()
	p.windows[window.ID] = window
	p.mu.Unlock()
}

func (p *recordingProbe) window(id uint32) windowlease.Window {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.windows[id]
}

func (p *recordingProbe) writeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writes
}

type recordingProbeMetrics struct {
	boundSnapshots  int
	elementPIDReads int
	positionReads   int
	sizeReads       int
}

func (p *recordingProbe) metrics() recordingProbeMetrics {
	p.mu.Lock()
	defer p.mu.Unlock()
	return recordingProbeMetrics{
		boundSnapshots:  p.boundSnapshots,
		elementPIDReads: p.elementPIDReads,
		positionReads:   p.positionReads,
		sizeReads:       p.sizeReads,
	}
}

func (p *recordingProbe) eventSource() *recordingEventSource {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.events
}

func (p *recordingProbe) sendEvent(event windowlease.WindowEvent) {
	p.mu.Lock()
	source := p.events
	p.mu.Unlock()
	if source == nil {
		panic("test event source is not ready")
	}
	source.events <- event
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
var _ interface {
	WindowByIDWithAX(int32, uint32, uintptr) (windowlease.Window, bool, error)
} = (*recordingProbe)(nil)

func waitWrite(t *testing.T, writes <-chan int) int {
	t.Helper()
	select {
	case write := <-writes:
		return write
	case <-time.After(time.Second):
		t.Fatal("window lease did not complete expected AX write")
		return 0
	}
}

func waitMaintenance(t *testing.T, maintained <-chan error) error {
	t.Helper()
	select {
	case err := <-maintained:
		return err
	case <-time.After(time.Second):
		t.Fatal("WeType lease maintenance did not finish")
		return context.DeadlineExceeded
	}
}

func waitProbeQuery(t *testing.T, queries <-chan struct{}) {
	t.Helper()
	select {
	case <-queries:
	case <-time.After(time.Second):
		t.Fatal("WeType lease did not inspect the bound window after AX event")
	}
}
