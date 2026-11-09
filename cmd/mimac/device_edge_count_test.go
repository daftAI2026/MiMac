package main

import (
	"errors"
	"testing"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
)

func deviceEdgeCountTestShell(t *testing.T, beforeStart func() error) (*shell, bridge.Device, *toolShortcutsTestSink) {
	t.Helper()
	s, _, keySink, device := remoteKeysTestShell(t)
	t.Cleanup(func() { _ = s.plugins.Close() })

	plan := voiceAudioTestPlan(t, bridge.ActionHold, bridge.RequestStart, bridge.RequestFinish, "RightCommand")
	d := s.coordinator
	d.voiceButton = "voice"
	d.audio = &routedEffectsAudioSink{}
	d.resolveVoiceAction = func() (bridge.ActionPlan, error) { return plan, nil }
	d.voiceAudioBinding = voiceAudioBindingForAction
	d.beforeAudioStart = beforeStart
	for _, event := range []bridge.Event{bridge.ScopeAvailable(true), bridge.RouteAvailable(true)} {
		if err := d.step(event); err != nil {
			t.Fatalf("configure test voice route: %v", err)
		}
	}
	return s, device, keySink
}

func TestHandleDeviceEventCountsRejectedStartDown(t *testing.T) {
	preflightErr := errors.New("textoutput: focused element is not editable")
	s, device, keySink := deviceEdgeCountTestShell(t, func() error { return preflightErr })
	press, err := bridge.NewPressToken(device, "voice", 1)
	if err != nil {
		t.Fatal(err)
	}

	s.handleDeviceEvent(devices.InputEvent{
		State: devices.InputStateConnected, Device: device,
		Edges: []devices.Edge{{Kind: devices.EdgeDown, Press: press}},
	})

	if got := s.report.ButtonDowns["voice"]; got != 1 {
		t.Fatalf("rejected down count=%d, want raw observed edge count 1", got)
	}
	if s.report.Error != "core event: "+preflightErr.Error() {
		t.Fatalf("preflight rejection was swallowed or changed: %q", s.report.Error)
	}
	if snapshot := s.coordinator.core.Snapshot(); snapshot.HasSession || len(snapshot.PressedButtons) != 1 || s.report.Core.ReceivedEdges != 1 {
		t.Fatalf("rejected start changed Core admission or lost edge: snapshot=%+v report=%+v", snapshot, s.report.Core)
	}
	if len(keySink.effects) != 0 || len(s.coordinator.audio.(*routedEffectsAudioSink).effects) != 0 {
		t.Fatalf("rejected start emitted target/audio effects: keys=%#v audio=%#v", keySink.effects, s.coordinator.audio.(*routedEffectsAudioSink).effects)
	}
}

func TestHandleDeviceEventCountsAcceptedDownUpOnce(t *testing.T) {
	s, device, _ := deviceEdgeCountTestShell(t, func() error { return nil })
	press, err := bridge.NewPressToken(device, "voice", 1)
	if err != nil {
		t.Fatal(err)
	}

	for _, kind := range []devices.EdgeKind{devices.EdgeDown, devices.EdgeUp} {
		s.handleDeviceEvent(devices.InputEvent{
			State: devices.InputStateConnected, Device: device,
			Edges: []devices.Edge{{Kind: kind, Press: press}},
		})
		if s.report.Error != "" {
			t.Fatalf("accepted edge %d unexpectedly failed: %q", kind, s.report.Error)
		}
	}
	if got := s.report.ButtonDowns["voice"]; got != 1 {
		t.Fatalf("accepted down count=%d, want 1", got)
	}
	if got := s.report.ButtonUps["voice"]; got != 1 {
		t.Fatalf("accepted up count=%d, want 1", got)
	}
	if s.report.Core.ReceivedEdges != 2 {
		t.Fatalf("accepted edge count=%d, want 2", s.report.Core.ReceivedEdges)
	}
}

func TestHandleDeviceEventDoesNotCountStaleGenerationEdge(t *testing.T) {
	s, retiredDevice, _ := deviceEdgeCountTestShell(t, func() error { return nil })
	currentDevice, err := bridge.NewDevice("key-lease-test", retiredDevice.Generation()+1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.coordinator.step(bridge.DeviceConnected(currentDevice)); err != nil {
		t.Fatalf("advance Core generation: %v", err)
	}
	press, err := bridge.NewPressToken(retiredDevice, "voice", 1)
	if err != nil {
		t.Fatal(err)
	}

	s.handleDeviceEvent(devices.InputEvent{
		State: devices.InputStateConnected, Device: retiredDevice,
		Edges: []devices.Edge{{Kind: devices.EdgeDown, Press: press}},
	})

	if got := s.report.ButtonDowns["voice"]; got != 0 {
		t.Fatalf("stale-generation down count=%d, want 0", got)
	}
	if s.report.Error != "" || s.report.Core.Generation != currentDevice.Generation() || s.report.Core.ReceivedEdges != 0 {
		t.Fatalf("stale event changed rejection/report semantics: error=%q core=%+v", s.report.Error, s.report.Core)
	}
}
