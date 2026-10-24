package main

import (
	"errors"
	"testing"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
	"mimac.local/mimac/internal/voicetools"
)

func TestReservedVoiceNeverFallsBackToStoredF5(t *testing.T) {
	d := newDeviceCoordinator()
	d.voiceButton = "voice"
	d.mappings = map[string][]bridge.Key{"voice": {"F5"}, "up": {"Up"}}
	d.resolveVoiceAction = func() (bridge.ActionPlan, error) { return bridge.ActionPlan{}, errors.New("not ready") }
	sink := &toolShortcutsTestSink{}
	if err := d.attachOutput(sink); err != nil {
		t.Fatal(err)
	}
	device, _ := bridge.NewDevice("test", 1)
	press, _ := bridge.NewPressToken(device, "voice", 1)
	if _, err := d.accept(devices.InputEvent{State: devices.InputStateConnected, Device: device, Edges: []devices.Edge{{Kind: devices.EdgeDown, Press: press}, {Kind: devices.EdgeUp, Press: press}}}); err != nil {
		t.Fatal(err)
	}
	if len(sink.effects) != 0 || d.report().Pressed != 0 {
		t.Fatal("reserved voice emitted fallback or retained press")
	}
	press, _ = bridge.NewPressToken(device, "up", 2)
	if _, err := d.accept(devices.InputEvent{State: devices.InputStateConnected, Device: device, Edges: []devices.Edge{{Kind: devices.EdgeDown, Press: press}, {Kind: devices.EdgeUp, Press: press}}}); err != nil {
		t.Fatal(err)
	}
	if len(sink.effects) != 2 {
		t.Fatal("ordinary mapping was suppressed with voice")
	}
}

func TestVoiceSelectionRequiresUnambiguousVerifiedDictation(t *testing.T) {
	s, _ := newToolShortcutsTestShell(t, toolShortcutsTestSnapshot("Ctrl+K"))
	s.deviceProfiles[0].VoiceButtonID = toolShortcutsTestButton
	if _, err := s.selectedVoiceAction(); err != nil {
		t.Fatal(err)
	}
	s.tools[0].snapshot.Actions[0].Control = nil
	if _, err := s.selectedVoiceAction(); err == nil {
		t.Fatal("unverified config became executable")
	}
	s.tools[0].snapshot = toolShortcutsTestSnapshot("Ctrl+K", "Ctrl+L")
	if _, err := s.selectedVoiceAction(); err == nil {
		t.Fatal("selected an arbitrary ambiguous shortcut")
	}
	s.tools[0].snapshot = toolShortcutsTestSnapshot("Ctrl+K")
	s.tools[0].snapshot.Actions[0].Scope = "app"
	if _, err := s.selectedVoiceAction(); err == nil {
		t.Fatal("app-scoped voice sent globally")
	}
	s.tools[0].snapshot.Actions[0].Scope = "global"
	s.tools[0].snapshot.Actions[0].Control = &voicetools.Control{Trigger: bridge.ActionTap, Request: bridge.RequestStart, ReleaseRequest: bridge.RequestNone}
	if _, err := s.selectedVoiceAction(); err == nil {
		t.Fatal("generic start action mistaken for dictation")
	}
}

func TestReservedVoiceHasNoExecutableOrdinaryFallback(t *testing.T) {
	d := newDeviceCoordinator()
	d.voiceButton = "voice"
	d.mappings = map[string][]bridge.Key{"voice": {"F5"}}
	if d.hasExecutableMapping() {
		t.Fatal("unused voice fallback requested output permission")
	}
}
