package main

import (
	"testing"

	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/bridge"
)

func TestDeviceConnectionDoesNotDependOnMappedOutput(t *testing.T) {
	s := &shell{coordinator: newDeviceCoordinator(), monitor: &testMonitor{}}
	device, err := bridge.NewDevice("test", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.coordinator.accept(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: device}); err != nil {
		t.Fatal(err)
	}
	s.deviceStatus = "按键处理异常"
	s.deviceReadBusy = true
	if s.remoteEnabled() || s.deviceConnectionLabel() != "已连接" {
		t.Fatal("connected hardware must not require enabled mapped output")
	}
	s.deviceReadBusy = false
	if _, err := s.coordinator.accept(xiaomi.MonitorEvent{State: xiaomi.MonitorStateDisconnected, Device: device}); err != nil {
		t.Fatal(err)
	}
	s.deviceStatus = "等待支持的遥控器"
	if got := s.deviceConnectionLabel(); got != "未连接" {
		t.Fatalf("disconnected hardware: %s", got)
	}
	s.monitor = nil
	s.deviceStatus = "已停止监听"
	if got := s.deviceConnectionLabel(); got != "尚未检测" {
		t.Fatalf("stopping observation cannot prove Bluetooth disconnection: %s", got)
	}
	s.deviceReadBusy = true
	if got := s.deviceConnectionLabel(); got != "正在检测" {
		t.Fatalf("pending discovery must not appear disconnected: %s", got)
	}
}
