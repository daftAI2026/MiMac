package main

import (
	"errors"
	"testing"

	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/pluginrt"
)

// This fixture uses the production shell/Core/power/remote callbacks with a
// test plugin runtime. Its output manifest is deliberately a fake sink; it
// never registers keyoutput.Open or can submit CGEvents.
func newPowerPermissionRecoveryShell(t *testing.T, connectAt ...int) (*shell, *powerTestMonitorFactory, chan func(), *[]*fakeKeySink) {
	t.Helper()
	factory := &powerTestMonitorFactory{
		connectAt: make(map[int]bool),
		failAt:    make(map[int]error),
	}
	for _, start := range connectAt {
		factory.connectAt[start] = true
	}
	profile, err := xiaomi.DeviceProfile()
	if err != nil {
		t.Fatal(err)
	}
	s := &shell{
		deviceProfiles:  []devicePlugin{{ModuleID: xiaomiPlugin, VoiceButtonID: xiaomi.VoiceButtonID, Profile: profile, Artwork: xiaomi.Artwork}},
		selectedProfile: profile.ID,
	}
	dispatches := make(chan func(), 32)
	s.remoteState.snapshotPath = t.TempDir() + "/voice-mapping-recovery.plist"
	s.remoteState.postMain = func(fn func()) { dispatches <- fn }
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) {
		factory.reservedFor = append(factory.reservedFor, s.coordinator.core.Snapshot().Generation)
		reservation := &remoteTestReservation{}
		factory.reservations = append(factory.reservations, reservation)
		return reservation, nil
	}
	var outputSinks []*fakeKeySink
	start := factory.start
	manifests := []pluginrt.Manifest{
		{ID: corePlugin, Start: func() (pluginrt.StopFunc, error) {
			s.coordinator = newDeviceCoordinator()
			return func() error {
				err := s.coordinator.disconnect()
				s.report.Core = s.coordinator.report()
				return err
			}, nil
		}},
		{ID: outputPlugin, Requires: []string{corePlugin}, Start: func() (pluginrt.StopFunc, error) {
			sink := &fakeKeySink{}
			if err := s.coordinator.attachOutput(sink); err != nil {
				return func() error { return nil }, err
			}
			s.output = sink
			outputSinks = append(outputSinks, sink)
			return func() error {
				err := s.coordinator.detachOutput()
				if err == nil {
					s.output = nil
				}
				return err
			}, nil
		}},
		{ID: xiaomiPlugin, Requires: []string{corePlugin}, Start: func() (pluginrt.StopFunc, error) {
			monitor, err := start(s.handleDeviceEvent)
			if err != nil {
				return func() error { return nil }, err
			}
			s.monitor = monitor
			s.report.MonitorStarts++
			return func() error {
				coreErr := s.coordinator.disconnect()
				s.report.Core = s.coordinator.report()
				nativeErr := monitor.Close()
				s.report.MonitorSamples = append(s.report.MonitorSamples, monitor.Stats())
				s.report.MonitorStopAttempts++
				if nativeErr == nil {
					s.monitor = nil
				}
				return errors.Join(nativeErr, coreErr)
			}, nil
		}},
		{ID: remotePlugin, Requires: []string{corePlugin, xiaomiPlugin}, Start: func() (pluginrt.StopFunc, error) {
			stop, err := s.remoteStart()
			return pluginrt.StopFunc(stop), err
		}},
	}
	runtime, err := pluginrt.New(manifests...)
	if err != nil {
		t.Fatal(err)
	}
	s.plugins = runtime
	if err := runtime.Enable(corePlugin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("close fake plugin runtime: %v", err)
		}
	})
	return s, factory, dispatches, &outputSinks
}

func TestPowerWakePermissionFailureCanRecoverAfterExplicitRetry(t *testing.T) {
	s, factory, queue, outputSinks := newPowerPermissionRecoveryShell(t, 1, 3)
	enablePowerTestRemote(t, s)
	if got := s.remoteState.reservationGeneration; got != 1 {
		t.Fatalf("initial reservation generation = %d, want 1", got)
	}
	coordinator := s.coordinator
	oldMonitor := factory.monitors[0]
	oldCallback := oldMonitor.callback
	oldReservation := factory.reservations[0]
	oldDevice, err := bridge.NewDevice("power-test-remote", 1)
	if err != nil {
		t.Fatal(err)
	}
	button := "hid:0007:0052"
	oldPress, err := bridge.NewPressToken(oldDevice, button, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.plugins.Enable(outputPlugin); err != nil {
		t.Fatal(err)
	}
	oldSink := (*outputSinks)[0]
	coordinator.mappings = map[string][]bridge.Key{button: {"Up"}}
	s.handleDeviceEvent(xiaomi.MonitorEvent{
		State:  xiaomi.MonitorStateConnected,
		Device: oldDevice,
		Edges:  []xiaomi.Edge{{Kind: xiaomi.EdgeDown, Press: oldPress}},
	})
	if len(oldSink.effects) != 1 {
		t.Fatalf("old-generation key start effects = %#v, want one key-down", oldSink.effects)
	}
	if _, ok := oldSink.effects[0].(bridge.KeyDownEffect); !ok {
		t.Fatalf("old-generation start effect = %#v, want key-down", oldSink.effects[0])
	}

	// Sleep tears down the old monitor and lease. The first quiet wake attempt
	// sees Input Monitoring denied, as it must never prompt without user action.
	factory.failAt[2] = xiaomi.ErrInputMonitoringRequired
	s.setPowerSuspended(true)
	pumpPowerDispatches(t, queue, func() bool { return !s.remoteBusy() && !s.remoteEnabled() })
	if oldReservation.closeCalls != 1 || s.remoteState.reservation != nil || oldMonitor.closeCalls != 1 ||
		coordinator.report().Pressed != 0 || len(coordinator.core.Snapshot().HeldKeys) != 0 {
		t.Fatalf("sleep retained old hardware ownership: reservation closes=%d current=%v monitor closes=%d core=%+v",
			oldReservation.closeCalls, s.remoteState.reservation != nil, oldMonitor.closeCalls, coordinator.core.Snapshot())
	}
	if len(oldSink.effects) != 2 {
		t.Fatalf("sleep did not release the old key exactly once: effects=%#v", oldSink.effects)
	}
	if _, ok := oldSink.effects[1].(bridge.KeyUpEffect); !ok || oldSink.closes != 1 {
		t.Fatalf("sleep cleanup effects/close = %#v/%d, want one old key-up and one fake sink close", oldSink.effects, oldSink.closes)
	}
	if s.output != nil || s.plugins.Active(outputPlugin) {
		t.Fatal("sleep cleanup retained the old fake output plugin")
	}
	s.setPowerSuspended(false)
	if factory.starts != 2 || s.remoteEnabled() || s.plugins.Active(xiaomiPlugin) ||
		!s.deviceNeedsPermission || s.deviceNeedsRestart || s.powerInputBlocked() || s.remoteBusy() {
		t.Fatalf("denied wake did not remain retryable and fail closed: starts=%d remote=%v monitor=%v permission=%v restart=%v blocked=%v busy=%v err=%v",
			factory.starts, s.remoteEnabled(), s.plugins.Active(xiaomiPlugin), s.deviceNeedsPermission,
			s.deviceNeedsRestart, s.powerInputBlocked(), s.remoteBusy(), s.remoteState.err)
	}
	if got := s.remoteLabel(); got != "需要输入监控权限" {
		t.Fatalf("permission failure status = %q, want actionable permission status", got)
	}

	// Duplicate wake notification must not prompt or silently retry. Keep the
	// mapping out of remote enable so refreshRemoteOutput cannot open any output
	// adapter; the fake output plugin is enabled explicitly below.
	s.setPowerSuspended(false)
	if factory.starts != 2 {
		t.Fatalf("duplicate wake retried monitor start: starts=%d, want 2", factory.starts)
	}
	delete(coordinator.mappings, button)
	s.toggleRemote()
	if factory.starts != 3 || !s.remoteEnabled() || !s.plugins.Active(xiaomiPlugin) ||
		s.deviceNeedsPermission || s.deviceNeedsRestart || s.remoteState.err != nil {
		t.Fatalf("explicit retry did not recover the host: starts=%d remote=%v monitor=%v permission=%v restart=%v err=%v",
			factory.starts, s.remoteEnabled(), s.plugins.Active(xiaomiPlugin), s.deviceNeedsPermission,
			s.deviceNeedsRestart, s.remoteState.err)
	}
	if s.coordinator != coordinator {
		t.Fatal("permission recovery replaced the running Core instance")
	}
	if snapshot := s.coordinator.core.Snapshot(); !snapshot.Connected || snapshot.Generation != 3 ||
		s.remoteState.reservation == nil || s.remoteState.reservationGeneration != snapshot.Generation {
		t.Fatalf("retry did not restore a connected fresh generation and matching voice lease: core=%+v reservation=%d owned=%v",
			snapshot, s.remoteState.reservationGeneration, s.remoteState.reservation != nil)
	}
	if len(factory.reservations) != 2 || factory.reservations[1] == oldReservation || factory.reservations[1].closeCalls != 0 {
		t.Fatalf("recovery did not acquire a fresh voice-key lease: reservations=%d old closes=%d new=%v",
			len(factory.reservations), oldReservation.closeCalls,
			len(factory.reservations) == 2 && factory.reservations[1] != oldReservation)
	}
	if s.audioSession != nil || s.liveAudio.connection != nil {
		t.Fatal("no-route fixture unexpectedly started an audio session")
	}

	coordinator.mappings[button] = []bridge.Key{"Up"}
	if err := s.plugins.Enable(outputPlugin); err != nil {
		t.Fatalf("enable fake output plugin: %v", err)
	}
	newSink := (*outputSinks)[1]
	newMonitor := factory.monitors[1] // The denied attempt creates no monitor owner.
	newDevice, err := bridge.NewDevice("power-test-remote", 3)
	if err != nil {
		t.Fatal(err)
	}
	newPress, err := bridge.NewPressToken(newDevice, button, 1)
	if err != nil {
		t.Fatal(err)
	}
	newMonitor.emit(xiaomi.MonitorEvent{
		State:  xiaomi.MonitorStateConnected,
		Device: newDevice,
		Edges:  []xiaomi.Edge{{Kind: xiaomi.EdgeDown, Press: newPress}},
	})
	if len(newSink.effects) != 1 {
		t.Fatalf("new-generation button down effects = %#v, want one key-down at fake output sink", newSink.effects)
	}
	if _, ok := newSink.effects[0].(bridge.KeyDownEffect); !ok {
		t.Fatalf("new-generation down effect = %#v, want key-down", newSink.effects[0])
	}
	newMonitor.emit(xiaomi.MonitorEvent{
		State:  xiaomi.MonitorStateConnected,
		Device: newDevice,
		Edges:  []xiaomi.Edge{{Kind: xiaomi.EdgeUp, Press: newPress}},
	})
	if len(newSink.effects) != 2 {
		t.Fatalf("new-generation button up effects = %#v, want one key-up", newSink.effects)
	}
	if _, ok := newSink.effects[1].(bridge.KeyUpEffect); !ok {
		t.Fatalf("new-generation up effect = %#v, want key-up", newSink.effects[1])
	}
	if coordinator.report().Pressed != 0 || len(coordinator.core.Snapshot().HeldKeys) != 0 {
		t.Fatalf("new-generation button release retained ownership: core=%+v", coordinator.core.Snapshot())
	}

	// A callback retained by the retired monitor must neither replace the new
	// Core generation nor produce another key effect after recovery.
	stalePress, err := bridge.NewPressToken(oldDevice, button, 2)
	if err != nil {
		t.Fatal(err)
	}
	oldCallback(xiaomi.MonitorEvent{
		State:  xiaomi.MonitorStateConnected,
		Device: oldDevice,
		Edges:  []xiaomi.Edge{{Kind: xiaomi.EdgeDown, Press: stalePress}},
	})
	if snapshot := coordinator.core.Snapshot(); snapshot.Generation != 3 || !snapshot.Connected ||
		coordinator.report().Pressed != 0 || len(newSink.effects) != 2 || s.remoteState.reservation != factory.reservations[1] {
		t.Fatalf("retired monitor affected resumed operation: core=%+v pressed=%d effects=%#v reservation=%v",
			snapshot, coordinator.report().Pressed, newSink.effects, s.remoteState.reservation)
	}
}
