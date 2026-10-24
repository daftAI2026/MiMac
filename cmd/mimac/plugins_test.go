package main

import (
	"errors"
	"testing"

	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/bridge"
)

type testMonitor struct {
	closes int
	err    error
}

func (m *testMonitor) Close() error               { m.closes++; return m.err }
func (m *testMonitor) Stats() xiaomi.MonitorStats { return xiaomi.MonitorStats{} }

func TestPluginPermissionRetryKeepsCore(t *testing.T) {
	s := &shell{}
	attempts := 0
	m := &testMonitor{}
	if err := s.setupPluginsWith(func(onEvent func(xiaomi.MonitorEvent)) (hidMonitor, error) {
		attempts++
		if attempts == 1 {
			return nil, xiaomi.ErrInputMonitoringRequired
		}
		device, _ := bridge.NewDevice("test", uint64(attempts))
		onEvent(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: device})
		return m, nil
	}); err != nil {
		t.Fatal(err)
	}
	core := s.coordinator
	s.toggleDeviceMonitor()
	if !s.deviceNeedsPermission || s.monitor != nil || s.plugins.Active(xiaomiPlugin) {
		t.Fatal("permission failure left a live or faulted listener")
	}
	s.toggleDeviceMonitor()
	if s.deviceNeedsPermission || s.monitor == nil || !s.coordinator.report().Connected || s.coordinator != core {
		t.Fatal("permission retry did not preserve and connect the core")
	}
	s.toggleDeviceMonitor()
	if m.closes != 1 || s.coordinator.report().Connected || s.plugins.Active(xiaomiPlugin) {
		t.Fatal("stop did not retire the connection")
	}
	if err := s.plugins.Close(); err != nil {
		t.Fatal(err)
	}
	if m.closes != 1 {
		t.Fatal("quit repeated listener cleanup")
	}
}

func TestPluginCleanupFailurePinsCoreAndPreventsRetry(t *testing.T) {
	s := &shell{}
	m := &testMonitor{err: xiaomi.ErrMonitorRestartRequired}
	attempts := 0
	if err := s.setupPluginsWith(func(onEvent func(xiaomi.MonitorEvent)) (hidMonitor, error) {
		attempts++
		device, _ := bridge.NewDevice("test", 1)
		press, _ := bridge.NewPressToken(device, "hid:0007:003e", 1)
		onEvent(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: device, Edges: []xiaomi.Edge{{Kind: xiaomi.EdgeDown, Press: press}}})
		return m, nil
	}); err != nil {
		t.Fatal(err)
	}
	s.toggleDeviceMonitor()
	s.toggleDeviceMonitor()
	if !s.deviceNeedsRestart || s.monitor != m || !s.plugins.Active(corePlugin) || !s.plugins.Active(xiaomiPlugin) || s.coordinator.report().Pressed != 0 {
		t.Fatalf("unsafe cleanup failure state: %+v", s.report)
	}
	s.toggleDeviceMonitor()
	if attempts != 1 || m.closes != 1 {
		t.Fatal("retried a faulted listener")
	}
	for range 2 {
		if err := s.plugins.Close(); !errors.Is(err, xiaomi.ErrMonitorRestartRequired) {
			t.Fatalf("lost cleanup error: %v", err)
		}
	}
	if m.closes != 1 {
		t.Fatal("quit repeated failed cleanup")
	}
}

func TestAudioOutputPluginIsLazyAndStopsItsOwnedResource(t *testing.T) {
	s := &shell{}
	if err := s.setupPluginsWith(func(func(xiaomi.MonitorEvent)) (hidMonitor, error) { t.Fatal("HID opened"); return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if s.audioOutput == nil || s.plugins.Active(audioOutputPlugin) {
		t.Fatal("audio module absent or auto-enabled")
	}
	if err := s.plugins.Enable(audioOutputPlugin); err != nil {
		t.Fatal(err)
	}
	if s.audioOutput.sink != nil || !s.audioOutput.enabled {
		t.Fatal("enabling audio opened a device")
	}
	owned := &audioOutputOwnerTestSink{}
	s.audioOutput.sink = owned
	if err := s.plugins.Close(); err != nil {
		t.Fatal(err)
	}
	if owned.closeCalls != 1 || s.audioOutput.sink != nil || s.audioOutput.enabled {
		t.Fatal("plugin close lost owned resource")
	}
}
