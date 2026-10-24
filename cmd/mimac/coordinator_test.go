package main

import (
	"errors"
	"testing"

	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
	"mimac.local/mimac/internal/voicetools"
)

func TestCoordinatorObservesRemoteWithoutActions(t *testing.T) {
	d := newDeviceCoordinator()
	device, _ := bridge.NewDevice("remote", 1)
	buttons := []string{"hid:0007:0052", "hid:0007:0051", "hid:0007:0050", "hid:0007:004f", "hid:0007:0028", "hid:0007:00f1", "hid:0007:004a", "hid:0007:0065", "hid:0007:003e", "hid:0007:0035", "hid:0007:0066", "hid:0007:0080", "hid:0007:0081"}
	for i, button := range buttons {
		press, _ := bridge.NewPressToken(device, button, uint64(i+1))
		for _, kind := range []xiaomi.EdgeKind{xiaomi.EdgeDown, xiaomi.EdgeUp} {
			accepted, err := d.accept(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: device, Edges: []xiaomi.Edge{{Kind: kind, Press: press}}})
			if err != nil || !accepted {
				t.Fatalf("%s %d: accepted=%v error=%v", button, kind, accepted, err)
			}
			want := 0
			if kind == xiaomi.EdgeDown {
				want = 1
			}
			if got := d.report().Pressed; got != want {
				t.Fatalf("%s %d: pressed=%d want=%d", button, kind, got, want)
			}
		}
	}
	s := d.core.Snapshot()
	if d.report().ReceivedEdges != 26 || s.Configured || s.HasSession || len(s.HeldKeys) != 0 || d.report().UnexpectedEffects != 0 {
		t.Fatalf("unsafe observation: report=%+v snapshot=%+v", d.report(), s)
	}
}

func TestCoordinatorShortcutRecordingIsolatesBindingsAndTracksRelease(t *testing.T) {
	d := newDeviceCoordinator()
	ordinaryButton := "hid:0007:0052"
	toolButton := "hid:0007:0051"
	d.mappings = map[string][]bridge.Key{
		ordinaryButton: {"Control", "K"},
		toolButton:     {"Return"}, // A stale ordinary entry must not run beside the action either.
	}
	ref := voicetools.ActionRef{ToolID: "test-tool", ActionID: "dictation", CandidateRaw: "Control+K"}
	d.actions = map[string]voicetools.ActionRef{toolButton: ref}
	plan, err := bridge.NewActionPlan(bridge.ActionSpec{
		ToolID: ref.ToolID, ActionID: ref.ActionID, CandidateRaw: ref.CandidateRaw,
		Revision: 1, Scope: bridge.ActionGlobal, Trigger: bridge.ActionTap,
		Request: bridge.RequestToggle, ReleaseRequest: bridge.RequestNone,
	}, "Control", "K")
	if err != nil {
		t.Fatal(err)
	}
	resolveCalls := 0
	d.resolveAction = func(got voicetools.ActionRef) (bridge.ActionPlan, error) {
		resolveCalls++
		if got != ref {
			t.Fatalf("resolved unexpected tool action: %#v", got)
		}
		return plan, nil
	}
	sink := &coordinatorTestSink{}
	if err := d.attachOutput(sink); err != nil {
		t.Fatal(err)
	}
	profile := devices.Profile{
		ID: "shortcut-recording-test", Name: "Test remote",
		Buttons: []devices.Button{{ID: ordinaryButton, Label: "ordinary"}, {ID: toolButton, Label: "tool"}},
	}
	s := &shell{
		settingsState:   settingsState{editButton: ordinaryButton},
		coordinator:     d,
		selectedProfile: profile.ID,
		deviceProfiles:  []devicePlugin{{Profile: profile}},
	}
	device, _ := bridge.NewDevice("recording-test", 1)
	s.handleDeviceEvent(devices.InputEvent{State: devices.InputStateConnected, Device: device})
	ordinary, _ := bridge.NewPressToken(device, ordinaryButton, 1)
	tool, _ := bridge.NewPressToken(device, toolButton, 2)

	s.handleDeviceEvent(devices.InputEvent{
		State: devices.InputStateConnected, Device: device,
		Edges: []devices.Edge{{Kind: devices.EdgeDown, Press: ordinary}, {Kind: devices.EdgeDown, Press: tool}},
	})
	if s.report.Error != "" || resolveCalls != 0 || sink.submitCalls != 0 || len(sink.effects) != 0 {
		t.Fatalf("recording invoked a binding: resolve=%d submits=%d effects=%#v", resolveCalls, sink.submitCalls, sink.effects)
	}
	if got := d.report().Pressed; got != 2 {
		t.Fatalf("recording stopped physical tracking: pressed=%d", got)
	}

	s.handleDeviceEvent(devices.InputEvent{
		State: devices.InputStateConnected, Device: device,
		Edges: []devices.Edge{{Kind: devices.EdgeUp, Press: ordinary}, {Kind: devices.EdgeUp, Press: tool}},
	})
	if s.report.Error != "" || d.report().Pressed != 0 || len(d.core.Snapshot().PressedButtons) != 0 || len(d.core.Snapshot().HeldKeys) != 0 {
		t.Fatalf("recording release left a core lease: report=%+v snapshot=%+v", d.report(), d.core.Snapshot())
	}
	if resolveCalls != 0 || sink.submitCalls != 0 || len(sink.effects) != 0 {
		t.Fatalf("recording release invoked output: resolve=%d submits=%d effects=%#v", resolveCalls, sink.submitCalls, sink.effects)
	}

	// A later physical press follows the unchanged normal mapping/action path.
	s.endShortcutRecording()
	ordinary, _ = bridge.NewPressToken(device, ordinaryButton, 3)
	tool, _ = bridge.NewPressToken(device, toolButton, 4)
	s.handleDeviceEvent(devices.InputEvent{
		State: devices.InputStateConnected, Device: device,
		Edges: []devices.Edge{{Kind: devices.EdgeDown, Press: ordinary}, {Kind: devices.EdgeUp, Press: ordinary}},
	})
	if s.report.Error != "" || sink.submitCalls == 0 || len(sink.effects) == 0 {
		t.Fatalf("ordinary mapping did not resume: error=%q submits=%d effects=%#v", s.report.Error, sink.submitCalls, sink.effects)
	}
	s.handleDeviceEvent(devices.InputEvent{
		State: devices.InputStateConnected, Device: device,
		Edges: []devices.Edge{{Kind: devices.EdgeDown, Press: tool}, {Kind: devices.EdgeUp, Press: tool}},
	})
	if s.report.Error != "" || resolveCalls != 1 {
		t.Fatalf("tool action did not resume: error=%q resolves=%d", s.report.Error, resolveCalls)
	}
}

func TestCoordinatorDisconnectAndRetiredGeneration(t *testing.T) {
	d := newDeviceCoordinator()
	first, _ := bridge.NewDevice("remote", 1)
	second, _ := bridge.NewDevice("remote", 2)
	voice, _ := bridge.NewPressToken(first, "hid:0007:003e", 1)
	volume, _ := bridge.NewPressToken(first, "hid:0007:0080", 2)
	_, err := d.accept(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: first, Edges: []xiaomi.Edge{{Kind: xiaomi.EdgeDown, Press: voice}, {Kind: xiaomi.EdgeDown, Press: volume}}})
	if err != nil || d.report().Pressed != 2 {
		t.Fatalf("mixed press: %+v %v", d.report(), err)
	}
	if err := d.disconnect(); err != nil || d.report().Pressed != 0 || d.report().Connected {
		t.Fatalf("stop: %+v %v", d.report(), err)
	}
	if ok, err := d.accept(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: first}); err != nil || ok {
		t.Fatalf("retired generation revived: %v %v", ok, err)
	}
	next, _ := bridge.NewPressToken(second, "hid:0007:003e", 1)
	_, err = d.accept(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: second, Edges: []xiaomi.Edge{{Kind: xiaomi.EdgeDown, Press: next}}})
	if err != nil || d.report().Pressed != 1 {
		t.Fatalf("restart: %+v %v", d.report(), err)
	}
	for _, state := range []string{xiaomi.MonitorStateConnected, xiaomi.MonitorStateDisconnected} {
		if ok, err := d.accept(xiaomi.MonitorEvent{State: state, Device: first, Edges: []xiaomi.Edge{{Kind: xiaomi.EdgeUp, Press: voice}}}); err != nil || ok {
			t.Fatalf("old callback accepted: %v %v", ok, err)
		}
	}
	if d.report().Pressed != 1 {
		t.Fatal("old release cleared new press")
	}
	_, err = d.accept(xiaomi.MonitorEvent{State: xiaomi.MonitorStateDisconnected, Device: second, Edges: []xiaomi.Edge{{Kind: xiaomi.EdgeUp, Press: next}}})
	if err != nil || d.report().Pressed != 0 || d.report().Connected || d.report().UnexpectedEffects != 0 {
		t.Fatalf("disconnect: %+v %v", d.report(), err)
	}
}

func TestCoordinatorRejectsMismatchedEnvelope(t *testing.T) {
	d := newDeviceCoordinator()
	device, _ := bridge.NewDevice("remote", 1)
	other, _ := bridge.NewDevice("other", 2)
	press, _ := bridge.NewPressToken(other, "hid:0007:003e", 1)
	if ok, err := d.accept(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: device, Edges: []xiaomi.Edge{{Kind: xiaomi.EdgeDown, Press: press}}}); err == nil || ok {
		t.Fatal("accepted mismatched connection")
	}
	if d.core.Snapshot().HasDevice || d.report().Steps != 0 {
		t.Fatal("malformed envelope partially mutated core")
	}
}

func TestCoordinatorLeavesUnmappedVoiceUsageObservedWithVoiceBinding(t *testing.T) {
	d := newDeviceCoordinator()
	binding, err := bridge.NewBinding(bridge.PolicyExactEdge, "Space")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{
		bridge.Configure(binding),
		bridge.RouteAvailable(true),
		bridge.ScopeAvailable(true),
	} {
		if _, err := d.core.Step(event); err != nil {
			t.Fatal(err)
		}
	}
	device, _ := bridge.NewDevice("remote", 1)
	press, _ := bridge.NewPressToken(device, "hid:0007:003e", 1)

	accepted, err := d.accept(xiaomi.MonitorEvent{
		State:  xiaomi.MonitorStateConnected,
		Device: device,
		Edges:  []xiaomi.Edge{{Kind: xiaomi.EdgeDown, Press: press}},
	})
	snapshot := d.core.Snapshot()
	if err != nil || !accepted || !snapshot.Configured || snapshot.HasSession || len(snapshot.PressedButtons) != 1 || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("unmapped voice usage started an action: accepted=%v err=%v report=%+v snapshot=%+v", accepted, err, d.report(), snapshot)
	}
	accepted, err = d.accept(xiaomi.MonitorEvent{
		State:  xiaomi.MonitorStateReportError,
		Device: device,
		Edges:  []xiaomi.Edge{{Kind: xiaomi.EdgeUp, Press: press}},
	})
	snapshot = d.core.Snapshot()
	if err != nil || !accepted || len(snapshot.PressedButtons) != 0 || snapshot.HasSession || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("observed voice usage did not release cleanly: accepted=%v err=%v snapshot=%+v", accepted, err, snapshot)
	}
}

type coordinatorTestSink struct {
	submitCalls     int
	releaseAllCalls int
	effects         []bridge.Effect
	failFirst       bool
}

func (s *coordinatorTestSink) Submit(effects []bridge.Effect) error {
	s.submitCalls++
	if s.failFirst {
		s.failFirst = false
		return errors.New("injected key output failure")
	}
	s.effects = append(s.effects, effects...)
	return nil
}

func (s *coordinatorTestSink) ReleaseAll() error {
	s.releaseAllCalls++
	return nil
}

func (s *coordinatorTestSink) Close() error       { return nil }
func (s *coordinatorTestSink) Held() []bridge.Key { return nil }

func TestCoordinatorReportErrorProcessesReleaseAfterOutputFailure(t *testing.T) {
	d := newDeviceCoordinator()
	d.mappings = map[string][]bridge.Key{"hid:0007:003e": {"Space"}}
	sink := &coordinatorTestSink{failFirst: true}
	if err := d.attachOutput(sink); err != nil {
		t.Fatal(err)
	}
	device, _ := bridge.NewDevice("remote", 1)
	if accepted, err := d.accept(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: device}); err != nil || !accepted {
		t.Fatalf("connect: accepted=%v err=%v", accepted, err)
	}
	press, _ := bridge.NewPressToken(device, "hid:0007:003e", 1)
	accepted, err := d.accept(xiaomi.MonitorEvent{
		State:  xiaomi.MonitorStateReportError,
		Device: device,
		Edges: []xiaomi.Edge{
			{Kind: xiaomi.EdgeDown, Press: press},
			{Kind: xiaomi.EdgeUp, Press: press},
		},
	})
	snapshot := d.core.Snapshot()
	if err == nil || !accepted || d.report().ReceivedEdges != 2 || len(snapshot.PressedButtons) != 0 || len(snapshot.HeldKeys) != 0 || snapshot.HasSession {
		t.Fatalf("report edges were not fully processed: accepted=%v err=%v report=%+v snapshot=%+v", accepted, err, d.report(), snapshot)
	}
	if snapshot.OutputFailureID != 1 || !snapshot.ScopeKnown || !snapshot.ScopeAvailable || snapshot.RouteKnown || !snapshot.Connected {
		t.Fatalf("output failure changed unrelated state: %+v", snapshot)
	}
	if sink.submitCalls != 1 || sink.releaseAllCalls != 1 || len(sink.effects) != 0 {
		t.Fatalf("output failure cleanup: submits=%d releaseAll=%d submitted=%v", sink.submitCalls, sink.releaseAllCalls, sink.effects)
	}
}
