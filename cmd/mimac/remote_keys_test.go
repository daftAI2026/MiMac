package main

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
	"mimac.local/mimac/internal/pluginrt"
	"mimac.local/mimac/internal/voicetools"
)

type remoteKeysTestReservation struct {
	remoteTestReservation
	updates [][]string
	err     error
}

func (r *remoteKeysTestReservation) UpdateKeys(buttons []string) error {
	r.updates = append(r.updates, append([]string(nil), buttons...))
	return r.err
}

func TestRemoteAcquisitionIncludesOnlyCustomProfileKeys(t *testing.T) {
	s, _ := setupRemoteTest(t, func(onEvent func(xiaomi.MonitorEvent), _ *remoteTestMonitor) error {
		onEvent(connectedEvent(t, 1))
		return nil
	})
	s.profile = emptyProfile()
	s.profile.Profiles[legacyXiaomiProfileID] = map[string]string{
		"hid:0007:0052": "A", "hid:0007:0051": "", "unknown": "B",
	}
	s.profile.ToolActions = map[string]map[string]voicetools.ActionRef{legacyXiaomiProfileID: {
		"hid:0007:0050": {ToolID: "tool", ActionID: "action", CandidateRaw: "Ctrl+K"},
	}}
	owner := &remoteKeysTestReservation{}
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) { return owner, nil }
	if err := s.plugins.Enable(remotePlugin); err != nil {
		t.Fatal(err)
	}
	want := []string{"hid:0007:0050", "hid:0007:0052"}
	if len(owner.updates) != 1 || !reflect.DeepEqual(owner.updates[0], want) {
		t.Fatalf("reserved sources = %v, want only custom known keys %v", owner.updates, want)
	}
}

func remoteKeysTestShell(t *testing.T) (*shell, *remoteKeysTestReservation, *toolShortcutsTestSink, bridge.Device) {
	t.Helper()
	s, _ := newToolShortcutsTestShell(t, toolShortcutsTestSnapshot("Ctrl+K"))
	s.mappingPath = filepath.Join(t.TempDir(), "mappings.json")
	runtime, err := pluginrt.New(
		pluginrt.Manifest{ID: remotePlugin, Start: func() (pluginrt.StopFunc, error) { return func() error { return nil }, nil }},
		pluginrt.Manifest{ID: outputPlugin, Start: func() (pluginrt.StopFunc, error) { return func() error { return nil }, nil }},
	)
	if err != nil {
		t.Fatal(err)
	}
	s.plugins = runtime
	if err := runtime.Enable(remotePlugin); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Enable(outputPlugin); err != nil {
		t.Fatal(err)
	}
	owner := &remoteKeysTestReservation{}
	s.remoteState.reservation = owner
	s.remoteState.reservationGeneration = 1
	device, err := bridge.NewDevice("key-lease-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.coordinator.accept(devices.InputEvent{State: devices.InputStateConnected, Device: device}); err != nil {
		t.Fatal(err)
	}
	sink := &toolShortcutsTestSink{}
	if err := s.coordinator.attachOutput(sink); err != nil {
		t.Fatal(err)
	}
	return s, owner, sink, device
}

func TestRemoteKeyMappingUpdatesLeaseAndRestoresDefault(t *testing.T) {
	s, owner, _, _ := remoteKeysTestShell(t)
	s.saveMapping(toolShortcutsTestButton, "A")
	s.saveMapping(toolShortcutsTestButton, "")
	want := [][]string{{toolShortcutsTestButton}, nil}
	if !reflect.DeepEqual(owner.updates, want) {
		t.Fatalf("lease updates = %v, want %v", owner.updates, want)
	}
}

func TestRemoteKeyToolActionUsesSameLease(t *testing.T) {
	s, owner, _, _ := remoteKeysTestShell(t)
	s.saveToolAction(toolShortcutsTestButton, voicetools.ActionRef{ToolID: "voice-typeless", ActionID: "dictationMode", CandidateRaw: "Ctrl+K"})
	if len(owner.updates) != 1 || !reflect.DeepEqual(owner.updates[0], []string{toolShortcutsTestButton}) {
		t.Fatalf("tool action did not take over its physical source: %v", owner.updates)
	}
}

func TestRemoteKeyReservationFailureBlocksNewOutputButReleasesHeldKey(t *testing.T) {
	s, owner, sink, device := remoteKeysTestShell(t)
	s.saveMapping(toolShortcutsTestButton, "A")
	press, _ := bridge.NewPressToken(device, toolShortcutsTestButton, 1)
	s.handleDeviceEvent(devices.InputEvent{Device: device, Edges: []devices.Edge{{Kind: devices.EdgeDown, Press: press}}})
	if len(sink.effects) != 1 {
		t.Fatalf("positive case did not emit A down: %v", sink.effects)
	}
	s.remoteState.err = errors.New("native mapping ownership changed")
	s.handleDeviceEvent(devices.InputEvent{Device: device, Edges: []devices.Edge{{Kind: devices.EdgeUp, Press: press}}})
	if len(sink.effects) != 2 {
		t.Fatalf("lost frozen release: %v", sink.effects)
	}
	press, _ = bridge.NewPressToken(device, toolShortcutsTestButton, 2)
	s.handleDeviceEvent(devices.InputEvent{Device: device, Edges: []devices.Edge{{Kind: devices.EdgeDown, Press: press}, {Kind: devices.EdgeUp, Press: press}}})
	if len(sink.effects) != 2 {
		t.Fatalf("unreserved key still injected output: %v", sink.effects)
	}
	owner.err = errors.New("lease write failed")
	s.remoteState.err = nil
	s.saveMapping(toolShortcutsTestButton, "B")
	if s.remoteState.err == nil || s.mappingError == "" {
		t.Fatal("failed takeover was advertised as active")
	}
	if s.remoteState.reservation != owner {
		t.Fatal("failed takeover lost its cleanup owner")
	}
}

func TestRemoteKeyMappingSaveFailureDoesNotChangeNativeLease(t *testing.T) {
	s, owner, _, _ := remoteKeysTestShell(t)
	s.mappingPath = t.TempDir()
	s.saveMapping(toolShortcutsTestButton, "A")
	if len(owner.updates) != 0 || s.mappingError == "" {
		t.Fatalf("save failure mutated native lease: %v, %q", owner.updates, s.mappingError)
	}
}
