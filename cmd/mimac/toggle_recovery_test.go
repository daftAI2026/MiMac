package main

import (
	"errors"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
	"reflect"
	"testing"
)

func TestToggleRecoveryWarningSurvivesRemoteStop(t *testing.T) {
	d := newDeviceCoordinator()
	if err := d.step(bridge.OutputFailed(1, "RightOption")); err != nil {
		t.Fatal(err)
	}
	if err := d.step(bridge.OutputRecovered(1)); err != nil {
		t.Fatal(err)
	}
	s := &shell{coordinator: d}
	label, tone := s.remoteStatus()
	if label != "请确认语音工具已结束" || tone != toneWarning {
		t.Fatalf("retained toggle gate hidden: status=%q/%v recovery=%v", label, tone, d.core.Snapshot().RecoveryKeys)
	}
}

func uncertainToggleShell(t *testing.T) *shell {
	t.Helper()
	d := newDeviceCoordinator()
	if err := d.step(bridge.OutputFailed(1, "RightOption")); err != nil {
		t.Fatal(err)
	}
	if err := d.step(bridge.OutputRecovered(1)); err != nil {
		t.Fatal(err)
	}
	return &shell{coordinator: d}
}

func TestToggleRecoveryRequiresManualConfirmation(t *testing.T) {
	s := uncertainToggleShell(t)
	receipt, ok := s.captureToggleRecovery()
	if !ok {
		t.Fatal("idle local host cannot request manual recovery")
	}
	before := s.coordinator.steps
	if s.confirmToggleRecovery(receipt, false) || !s.toggleRecoveryNeeded() || s.coordinator.steps != before {
		t.Fatal("cancel cleared uncertainty")
	}
	if !s.confirmToggleRecovery(receipt, true) || s.toggleRecoveryNeeded() {
		t.Fatal("explicit confirmation did not clear retained gate")
	}
	if s.confirmToggleRecovery(receipt, true) {
		t.Fatal("old acknowledgement was accepted twice")
	}
	if len(s.coordinator.report().RecoveryKeys) != 0 {
		t.Fatal("report retained cleared recovery")
	}
}

func TestToggleRecoveryRejectsChangedState(t *testing.T) {
	cases := []struct {
		name   string
		change func(*shell)
	}{
		{"closing", func(s *shell) { s.closing = true }},
		{"cleanup pending", func(s *shell) { s.remoteState.cleanupInFlight = true }},
		{"cleanup failed", func(s *shell) { s.report.AudioCleanupFailed = true }},
		{"route retiring", func(s *shell) { s.liveAudio.retiring = true }},
		{"audio owner", func(s *shell) { s.audioSession = &audioSessionHost{} }},
		{"route owner", func(s *shell) { s.liveAudio.connection = &liveAudioConnection{} }},
		{"remote error", func(s *shell) { s.remoteState.err = errors.New("cleanup failed") }},
		{"output restart", func(s *shell) { s.outputNeedsRestart = true }},
		{"new core", func(s *shell) { s.coordinator.core = uncertainToggleShell(t).coordinator.core }},
		{"new coordinator", func(s *shell) { s.coordinator = uncertainToggleShell(t).coordinator }},
		{"new event", func(s *shell) {
			if err := s.coordinator.step(bridge.ScopeAvailable(false)); err != nil {
				t.Fatal(err)
			}
		}},
		{"new uncertainty", func(s *shell) {
			if err := s.coordinator.step(bridge.OutputFailed(2, "RightCommand")); err != nil {
				t.Fatal(err)
			}
			if err := s.coordinator.step(bridge.OutputRecovered(2)); err != nil {
				t.Fatal(err)
			}
		}},
		{"output failure", func(s *shell) {
			if err := s.coordinator.step(bridge.OutputFailed(2)); err != nil {
				t.Fatal(err)
			}
		}},
		{"native held", func(s *shell) { s.coordinator.sink = &heldCoordinatorTestSink{held: []bridge.Key{"RightCommand"}} }},
		{"remote enabled", func(s *shell) {
			var err error
			s.plugins, err = remoteTestPluginRuntime(func() error { return nil })
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"pressed button", func(s *shell) {
			d, _ := bridge.NewDevice("test", 1)
			p, _ := bridge.NewPressToken(d, "button", 1)
			if err := s.coordinator.step(bridge.DeviceConnected(d)); err != nil {
				t.Fatal(err)
			}
			if err := s.coordinator.step(bridge.ObservedBegan(p)); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s := uncertainToggleShell(t)
			receipt, ok := s.captureToggleRecovery()
			if !ok {
				t.Fatal("precondition")
			}
			tt.change(s)
			before := s.coordinator.core.Snapshot().RecoveryKeys
			if s.confirmToggleRecovery(receipt, true) || !reflect.DeepEqual(before, s.coordinator.core.Snapshot().RecoveryKeys) {
				t.Fatal("stale or unsafe acknowledgement cleared gate")
			}
		})
	}
}

func TestToggleRecoveryRestartsOnlyOnNextPhysicalPress(t *testing.T) {
	plan := voiceAudioTestPlan(t, bridge.ActionTap, bridge.RequestToggle, bridge.RequestNone, "RightOption")
	d, device, order := newVoiceAudioTestCoordinator(t, plan)
	press := voiceAudioTestPress(t, device, 1)
	acceptVoiceAudioEdge(t, d, device, press, devices.EdgeDown)
	token := d.core.Snapshot().Session.Token
	if err := d.step(bridge.SourcePrepared(token, "fake-source")); err != nil {
		t.Fatal(err)
	}
	if err := d.step(bridge.SourceFailed(token)); err != nil {
		t.Fatal(err)
	}
	s := &shell{coordinator: d}
	if _, ok := s.captureToggleRecovery(); ok {
		t.Fatal("unreleased button offered confirmation")
	}
	acceptVoiceAudioEdge(t, d, device, press, devices.EdgeUp)
	if err := d.disconnect(); err != nil {
		t.Fatal(err)
	}
	if !s.toggleRecoveryNeeded() {
		t.Fatal("disconnect cleared uncertain target")
	}
	receipt, ok := s.captureToggleRecovery()
	if !ok {
		t.Fatal("local cleanup did not permit manual confirmation")
	}
	before := append([]string(nil), (*order)...)
	if !s.confirmToggleRecovery(receipt, true) {
		t.Fatal("explicit confirmation rejected")
	}
	if !reflect.DeepEqual(before, *order) {
		t.Fatal("confirmation emitted target/audio effects")
	}
	if got := len(voiceAudioTargetTaps(d.sink.(*routedEffectsKeySink))); got != 1 {
		t.Fatalf("unexpected taps after confirmation: %d", got)
	}
	device2, _ := bridge.NewDevice(device.ID(), device.Generation()+1)
	if err := d.step(bridge.DeviceConnected(device2)); err != nil {
		t.Fatal(err)
	}
	press2 := voiceAudioTestPress(t, device2, 1)
	acceptVoiceAudioEdge(t, d, device2, press2, devices.EdgeDown)
	if !d.core.Snapshot().HasSession || len(voiceAudioTargetTaps(d.sink.(*routedEffectsKeySink))) != 2 {
		t.Fatal("next physical press did not create exactly one new start")
	}
}

func TestToggleRecoveryDialogDefaultsToCancel(t *testing.T) {
	opts := toggleRecoveryMessage(nil)
	if opts.DefaultButton != 0 || opts.CancelButton != 0 || len(opts.Buttons) != 2 || opts.Buttons[0] != "取消" || opts.Buttons[1] != "已结束，恢复" {
		t.Fatalf("unsafe confirmation options: %+v", opts)
	}
}

func TestToggleRecoveryAfterPartialNonAudioTap(t *testing.T) {
	d, device := newEffectsTestCoordinator(t, bridge.PolicyExactEdge, "Space")
	d.audio = nil
	sink := d.sink.(*routedEffectsKeySink)
	sink.failAt = map[int]error{1: errors.New("partial target tap")}
	press, _ := bridge.NewPressToken(device, "tool", 1)
	plan := voiceAudioTestPlan(t, bridge.ActionTap, bridge.RequestToggle, bridge.RequestNone, "RightOption")
	if err := d.step(bridge.TargetActionBegan(press, plan)); err == nil {
		t.Fatal("partial tap did not fail")
	}
	s := &shell{coordinator: d}
	if !s.toggleRecoveryNeeded() {
		t.Fatal("partial tap lost uncertainty")
	}
	if _, ok := s.captureToggleRecovery(); ok {
		t.Fatal("output failure not yet released")
	}
	if err := d.step(bridge.OrdinaryEnded(press)); err != nil {
		t.Fatal(err)
	}
	if err := d.step(bridge.OutputRecovered(d.core.Snapshot().OutputFailureID)); err != nil {
		t.Fatal(err)
	}
	if err := d.disconnect(); err != nil {
		t.Fatal(err)
	}
	receipt, ok := s.captureToggleRecovery()
	if !ok {
		t.Fatal("non-PCM action cannot be manually recovered")
	}
	before := len(sink.batches)
	if !s.confirmToggleRecovery(receipt, true) || len(sink.batches) != before {
		t.Fatal("manual recovery emitted target input")
	}
}

func TestToggleRecoveryUsesExistingRemoteAction(t *testing.T) {
	s := uncertainToggleShell(t)
	if s.remoteButtonLabel() != toggleRecoveryLabel || s.remoteButtonDisabled() {
		t.Fatal("stopped remote has no usable recovery action")
	}
	var err error
	s.plugins, err = remoteTestPluginRuntime(func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if s.remoteButtonLabel() != "停用遥控器" || s.remoteButtonDisabled() {
		t.Fatal("uncertainty blocked the existing cleanup action")
	}
	if got := s.remoteLabel(); got != "请先停用遥控器并结束语音" {
		t.Fatalf("missing next step: %q", got)
	}
}
