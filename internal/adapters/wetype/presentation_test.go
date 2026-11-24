package wetype

import (
	"context"
	"errors"
	"testing"

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
	windows map[uint32]windowlease.Window
	writes  int
}

func newRecordingProbe() *recordingProbe {
	return &recordingProbe{windows: make(map[uint32]windowlease.Window)}
}

func (p *recordingProbe) WindowNumbers(pid int32) (map[uint32]struct{}, error) {
	ids := make(map[uint32]struct{})
	for id, window := range p.windows {
		if window.OwnerPID == pid {
			ids[id] = struct{}{}
		}
	}
	return ids, nil
}

func (p *recordingProbe) Windows(pid int32, newerThan map[uint32]struct{}) ([]windowlease.Window, error) {
	var result []windowlease.Window
	for id, window := range p.windows {
		if window.OwnerPID != pid {
			continue
		}
		if _, exists := newerThan[id]; !exists {
			result = append(result, window)
		}
	}
	return result, nil
}

func (p *recordingProbe) WindowByID(pid int32, id uint32) (windowlease.Window, bool, error) {
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

var _ windowlease.Probe = (*recordingProbe)(nil)
