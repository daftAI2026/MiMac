package main

import (
	"errors"
	"reflect"
	"testing"

	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/bridge"
)

type fakeKeySink struct {
	effects  []bridge.Effect
	fail     bool
	cleanups int
	closes   int
}

func (s *fakeKeySink) Submit(e []bridge.Effect) error {
	if s.fail {
		return errors.New("injected output failure")
	}
	s.effects = append(s.effects, e...)
	return nil
}
func (s *fakeKeySink) ReleaseAll() error  { s.cleanups++; return nil }
func (s *fakeKeySink) Close() error       { s.closes++; return nil }
func (s *fakeKeySink) Held() []bridge.Key { return nil }

func TestMappedVoiceButtonIsOrdinaryWithoutAudioAndStopsCleanly(t *testing.T) {
	d := newDeviceCoordinator()
	sink := &fakeKeySink{}
	if err := d.attachOutput(sink); err != nil {
		t.Fatal(err)
	}
	d.mappings = map[string][]bridge.Key{"hid:0007:003e": {"Control", "Space"}}
	device, _ := bridge.NewDevice("remote", 1)
	press, _ := bridge.NewPressToken(device, "hid:0007:003e", 1)
	_, err := d.accept(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: device, Edges: []xiaomi.Edge{{Kind: xiaomi.EdgeDown, Press: press}}})
	if err != nil {
		t.Fatal(err)
	}
	if d.core.Snapshot().HasSession {
		t.Fatal("ordinary shortcut opened voice session")
	}
	if err = d.detachOutput(); err != nil {
		t.Fatal(err)
	}
	want := []bridge.Effect{bridge.KeyDownEffect{Key: "Control"}, bridge.KeyDownEffect{Key: "Space"}, bridge.KeyUpEffect{Key: "Space"}, bridge.KeyUpEffect{Key: "Control"}}
	if !reflect.DeepEqual(sink.effects, want) || sink.closes != 1 {
		t.Fatalf("stop: %#v closes=%d", sink.effects, sink.closes)
	}
	if len(d.core.Snapshot().HeldKeys) != 0 || d.report().Pressed != 1 {
		t.Fatal("stop discarded actual press or left lease")
	}
}

func TestOutputFailureDoesNotDropOtherEdgesInSameReport(t *testing.T) {
	d := newDeviceCoordinator()
	sink := &fakeKeySink{fail: true}
	_ = d.attachOutput(sink)
	d.mappings = map[string][]bridge.Key{"hid:0007:0052": {"Up"}, "hid:0007:0051": {"Down"}}
	device, _ := bridge.NewDevice("remote", 1)
	first, _ := bridge.NewPressToken(device, "hid:0007:0052", 1)
	second, _ := bridge.NewPressToken(device, "hid:0007:0051", 2)
	_, err := d.accept(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: device, Edges: []xiaomi.Edge{{Kind: xiaomi.EdgeDown, Press: first}, {Kind: xiaomi.EdgeDown, Press: second}}})
	if err == nil || d.report().Pressed != 2 || !(d.report().OutputFailureID != 0) {
		t.Fatalf("failed report lost edges: %+v %v", d.report(), err)
	}
	sink.fail = false
	if err = d.step(bridge.OutputRecovered(d.report().OutputFailureID)); err != nil {
		t.Fatal(err)
	}
	_, err = d.accept(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: device, Edges: []xiaomi.Edge{{Kind: xiaomi.EdgeUp, Press: first}, {Kind: xiaomi.EdgeUp, Press: second}}})
	if err != nil || d.report().Pressed != 0 || d.report().OutputFailureID != 0 || len(sink.effects) != 0 {
		t.Fatalf("recovery replayed or stayed stuck: %+v", d.report())
	}
}
