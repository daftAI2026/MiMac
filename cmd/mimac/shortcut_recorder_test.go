package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
)

func TestShortcutLabelPreservesRightModifierNames(t *testing.T) {
	for value, want := range map[string]string{
		"RightOption": "右⌥", "RightCommand": "右⌘", "RightCommand+Right": "右⌘→",
		"Control+Shift+Space": "⌃⇧空格", "Command+C": "⌘C", "Right": "→",
		"UnverifiedRightName": "UnverifiedRightName", "": "不设置",
	} {
		if got := shortcutLabel(value); got != want {
			t.Errorf("%q: got %q, want %q", value, got, want)
		}
	}
}

func TestRecorderUsesPhysicalPositionAndIgnoresRepeats(t *testing.T) {
	value, ok := recordedShortcut(ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyZ, PhysicalKey: ui.KeyY, Mods: ui.Ctrl | ui.Shift})
	if !ok || value != "Control+Shift+Y" {
		t.Fatalf("wrong physical mapping: %q %v", value, ok)
	}
	if _, ok := recordedShortcut(ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyY, Repeat: true}); ok {
		t.Fatal("recorded repeat")
	}
	if _, ok := recordedShortcut(ui.InputEvent{Kind: ui.InputKeyUp, Key: ui.KeyY}); ok {
		t.Fatal("recorded release")
	}
}

func TestRecorderNeverCommitsModifiersOrText(t *testing.T) {
	for _, event := range []ui.InputEvent{
		{Kind: ui.InputKeyDown, Mods: ui.Ctrl},
		{Kind: ui.InputText},
		{Kind: ui.InputKeyUp, Key: ui.KeyC, Mods: ui.Super},
	} {
		if value, ok := recordedShortcut(event); ok {
			t.Fatalf("unexpected shortcut %q from %+v", value, event)
		}
	}
}

func TestShortcutRecorderWaitsForExistingRemotePressToRelease(t *testing.T) {
	d := newDeviceCoordinator()
	button := "hid:0007:0052"
	d.mappings = map[string][]bridge.Key{button: {"Control", "K"}}
	sink := &coordinatorTestSink{}
	if err := d.attachOutput(sink); err != nil {
		t.Fatal(err)
	}
	device, _ := bridge.NewDevice("recorder-entry-test", 1)
	press, _ := bridge.NewPressToken(device, button, 1)
	if accepted, err := d.accept(devices.InputEvent{State: devices.InputStateConnected, Device: device, Edges: []devices.Edge{{Kind: devices.EdgeDown, Press: press}}}); err != nil || !accepted {
		t.Fatalf("existing mapping down: accepted=%v err=%v", accepted, err)
	}
	if d.report().Pressed != 1 || len(sink.effects) == 0 {
		t.Fatalf("pre-entry action was swallowed: report=%+v effects=%#v", d.report(), sink.effects)
	}

	s := &shell{settingsState: settingsState{}, coordinator: d}
	s.beginShortcutRecording(button)
	if s.editButton != "" || s.mappingError != "请先松开遥控器按键" {
		t.Fatalf("recorder entered with a live physical/output lease: edit=%q error=%q", s.editButton, s.mappingError)
	}

	if accepted, err := d.accept(devices.InputEvent{State: devices.InputStateConnected, Device: device, Edges: []devices.Edge{{Kind: devices.EdgeUp, Press: press}}}); err != nil || !accepted {
		t.Fatalf("pre-entry mapping release: accepted=%v err=%v", accepted, err)
	}
	if d.report().Pressed != 0 || len(d.core.Snapshot().PressedButtons) != 0 || len(d.core.Snapshot().HeldKeys) != 0 {
		t.Fatalf("pre-entry physical/output lease did not retire: report=%+v snapshot=%+v", d.report(), d.core.Snapshot())
	}
}

func TestShortcutRecorderRequiresCoreOutputLeaseToBeCleared(t *testing.T) {
	d := newDeviceCoordinator()
	binding, err := bridge.NewBinding(bridge.PolicyAfterDrain, "Space")
	if err != nil {
		t.Fatal(err)
	}
	device, _ := bridge.NewDevice("core-output-lease-test", 1)
	for _, event := range []bridge.Event{
		bridge.Configure(binding),
		bridge.RouteAvailable(true),
		bridge.ScopeAvailable(true),
		bridge.DeviceConnected(device),
	} {
		if _, err := d.core.Step(event); err != nil {
			t.Fatal(err)
		}
	}
	press, _ := bridge.NewPressToken(device, "voice", 1)
	effects, err := d.core.Step(bridge.VoiceBegan(press))
	if err != nil {
		t.Fatal(err)
	}
	var token bridge.SessionToken
	for _, effect := range effects {
		if prepared, ok := effect.(bridge.PrepareSourceEffect); ok {
			token = prepared.Token
		}
	}
	if token == (bridge.SessionToken{}) {
		t.Fatalf("voice begin did not create a source token: %#v", effects)
	}
	if _, err := d.core.Step(bridge.SourcePrepared(token, "resource")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.core.Step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	snapshot := d.core.Snapshot()
	if len(snapshot.PressedButtons) != 0 || len(snapshot.HeldKeys) == 0 {
		t.Fatalf("fixture did not leave only a Core output lease: %+v", snapshot)
	}

	s := &shell{settingsState: settingsState{}, coordinator: d}
	s.beginShortcutRecording("voice")
	if s.editButton != "" || s.mappingError != "请先停用按键输出，再录入快捷键" {
		t.Fatalf("recorder entered with a Core output lease: edit=%q error=%q", s.editButton, s.mappingError)
	}
}

type heldCoordinatorTestSink struct {
	coordinatorTestSink
	held []bridge.Key
}

func (s *heldCoordinatorTestSink) Held() []bridge.Key {
	return append([]bridge.Key(nil), s.held...)
}

func TestShortcutRecorderRequiresSinkOutputLeaseToBeCleared(t *testing.T) {
	d := newDeviceCoordinator()
	sink := &heldCoordinatorTestSink{held: []bridge.Key{"Control"}}
	if err := d.attachOutput(sink); err != nil {
		t.Fatal(err)
	}
	if snapshot := d.core.Snapshot(); len(snapshot.PressedButtons) != 0 || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("fixture unexpectedly has a Core lease: %+v", snapshot)
	}
	s := &shell{settingsState: settingsState{}, coordinator: d}
	s.beginShortcutRecording("button")
	if s.editButton != "" || s.mappingError != "请先停用按键输出，再录入快捷键" {
		t.Fatalf("recorder entered with a sink output lease: edit=%q error=%q", s.editButton, s.mappingError)
	}
}

func TestRemotePressReleasedAfterRecorderExitDoesNotEmitMapping(t *testing.T) {
	d := newDeviceCoordinator()
	button := "hid:0007:0052"
	d.mappings = map[string][]bridge.Key{button: {"Control", "K"}}
	sink := &coordinatorTestSink{}
	if err := d.attachOutput(sink); err != nil {
		t.Fatal(err)
	}
	profile := devices.Profile{
		ID: "late-release-test", Name: "Test remote",
		Buttons: []devices.Button{{ID: button, Label: "up"}},
	}
	s := &shell{
		settingsState:   settingsState{editButton: button},
		coordinator:     d,
		selectedProfile: profile.ID,
		deviceProfiles:  []devicePlugin{{Profile: profile}},
	}
	device, _ := bridge.NewDevice("late-release-test", 1)
	s.handleDeviceEvent(devices.InputEvent{State: devices.InputStateConnected, Device: device})
	press, _ := bridge.NewPressToken(device, button, 1)
	s.handleDeviceEvent(devices.InputEvent{
		State: devices.InputStateConnected, Device: device,
		Edges: []devices.Edge{{Kind: devices.EdgeDown, Press: press}},
	})
	if d.report().Pressed != 1 || len(sink.effects) != 0 || len(d.core.Snapshot().HeldKeys) != 0 {
		t.Fatalf("recording down was emitted or untracked: report=%+v effects=%#v snapshot=%+v", d.report(), sink.effects, d.core.Snapshot())
	}

	s.endShortcutRecording()
	s.handleDeviceEvent(devices.InputEvent{
		State: devices.InputStateConnected, Device: device,
		Edges: []devices.Edge{{Kind: devices.EdgeUp, Press: press}},
	})
	if s.report.Error != "" || d.report().Pressed != 0 || len(d.core.Snapshot().PressedButtons) != 0 ||
		len(d.core.Snapshot().HeldKeys) != 0 || len(sink.effects) != 0 || sink.submitCalls != 0 {
		t.Fatalf("late release re-evaluated the mapping or left a lease: error=%q report=%+v snapshot=%+v submits=%d effects=%#v",
			s.report.Error, d.report(), d.core.Snapshot(), sink.submitCalls, sink.effects)
	}
}
