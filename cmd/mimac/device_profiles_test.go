package main

import (
	"errors"
	"reflect"
	"testing"

	"mimac.local/mimac/internal/adapters/keyoutput"
	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
)

type profileSwitchMonitor struct {
	closeErr   error
	closeCalls int
}

func (m *profileSwitchMonitor) Close() error {
	m.closeCalls++
	return m.closeErr
}

func (*profileSwitchMonitor) Stats() xiaomi.MonitorStats { return xiaomi.MonitorStats{} }

type closeableProfileSink struct {
	held         []bridge.Key
	submitCalls  int
	failSubmitAt int
	closed       bool
	submitted    []bridge.Effect
	releases     []bridge.Effect
}

func (s *closeableProfileSink) Submit(effects []bridge.Effect) error {
	if s.closed {
		return errors.New("sink closed")
	}
	s.submitCalls++
	if s.submitCalls == s.failSubmitAt {
		return errors.New("injected submit failure")
	}
	_, next, err := keyoutput.PlanKeys(s.held, effects)
	if err != nil {
		return err
	}
	s.held = next
	s.submitted = append(s.submitted, effects...)
	return nil
}

func (s *closeableProfileSink) ReleaseAll() error {
	if s.closed {
		return errors.New("sink closed")
	}
	effects := make([]bridge.Effect, 0, len(s.held))
	for i := len(s.held) - 1; i >= 0; i-- {
		effects = append(effects, bridge.KeyUpEffect{Key: s.held[i]})
	}
	_, next, err := keyoutput.PlanKeys(s.held, effects)
	if err != nil {
		return err
	}
	s.held = next
	s.releases = append(s.releases, effects...)
	return nil
}

func (s *closeableProfileSink) Close() error {
	if err := s.ReleaseAll(); err != nil {
		return err
	}
	s.closed = true
	return nil
}

func (s *closeableProfileSink) Held() []bridge.Key {
	return append([]bridge.Key(nil), s.held...)
}

func profileSwitchFixture(id, buttonID string) devices.Profile {
	return devices.Profile{ID: id, Name: id, Buttons: []devices.Button{{ID: buttonID, Label: buttonID}}}
}

func profileMappingFixture(a, b devices.Profile) mappingProfile {
	store := emptyProfile()
	store.Profiles[a.ID] = map[string]string{a.Buttons[0].ID: "Up"}
	store.Profiles[b.ID] = map[string]string{b.Buttons[0].ID: "Down"}
	return store
}

func TestSwitchDeviceProfileUsesOnlySelectedProfileMappings(t *testing.T) {
	profileA := profileSwitchFixture("device-a", "button-a")
	profileB := profileSwitchFixture("device-b", "button-b")
	store := profileMappingFixture(profileA, profileB)
	keysA, err := store.keys(profileA)
	if err != nil {
		t.Fatal(err)
	}
	s := &shell{
		settingsState:   settingsState{profile: store, selectedButton: "button-a", editButton: "button-a"},
		coordinator:     newDeviceCoordinator(),
		deviceProfiles:  []devicePlugin{{ModuleID: "plugin-a", Profile: profileA}, {ModuleID: "plugin-b", Profile: profileB}},
		selectedProfile: profileA.ID,
	}
	s.coordinator.mappings = keysA

	if err := s.selectDeviceProfile(profileB.ID); err != nil {
		t.Fatal(err)
	}
	want, err := store.keys(profileB)
	if err != nil {
		t.Fatal(err)
	}
	if s.selectedProfile != profileB.ID || !reflect.DeepEqual(s.coordinator.mappings, want) {
		t.Fatalf("selected=%q mappings=%v, want profile %q mappings %v", s.selectedProfile, s.coordinator.mappings, profileB.ID, want)
	}
	if _, leaked := s.coordinator.mappings[profileA.Buttons[0].ID]; leaked {
		t.Fatalf("old profile mapping leaked into new profile: %v", s.coordinator.mappings)
	}
	if s.profile.Profiles[profileA.ID][profileA.Buttons[0].ID] != "Up" || s.profile.Profiles[profileB.ID][profileB.Buttons[0].ID] != "Down" {
		t.Fatalf("switch mutated profile store: %+v", s.profile.Profiles)
	}
	if s.selectedButton != "" || s.editButton != "" {
		t.Fatalf("switch kept old button editing state: selected=%q edit=%q", s.selectedButton, s.editButton)
	}
}

func TestUnknownDeviceProfileCannotReplaceSelectedIdentity(t *testing.T) {
	profile := profileSwitchFixture("xiaomi-rc003", "hid:0007:003e")
	s := &shell{
		deviceProfiles:  []devicePlugin{{ModuleID: xiaomiPlugin, Profile: profile}},
		selectedProfile: profile.ID,
		coordinator:     newDeviceCoordinator(),
	}

	if err := s.selectDeviceProfile("apple-tv"); err == nil {
		t.Fatal("unknown device profile was accepted")
	}
	if s.selectedProfile != profile.ID || s.activeProfile().ID != profile.ID {
		t.Fatalf("unknown selection changed active identity: selected=%q active=%q", s.selectedProfile, s.activeProfile().ID)
	}
}

func TestFaultedDeviceCleanupBlocksProfileSwitch(t *testing.T) {
	monitor := &profileSwitchMonitor{closeErr: xiaomi.ErrMonitorRestartRequired}
	s := &shell{}
	if err := s.setupPluginsWith(func(func(xiaomi.MonitorEvent)) (hidMonitor, error) {
		return monitor, nil
	}); err != nil {
		t.Fatal(err)
	}
	profileA := profileSwitchFixture("xiaomi-rc003", "hid:0007:0052")
	profileB := profileSwitchFixture("future-remote", "button:future")
	store := profileMappingFixture(profileA, profileB)
	s.deviceProfiles = []devicePlugin{
		{ModuleID: xiaomiPlugin, Profile: profileA},
		{ModuleID: "future-hid", Profile: profileB},
	}
	s.selectedProfile = profileA.ID
	s.profile = store
	s.coordinator.mappings, _ = store.keys(profileA)

	s.toggleDeviceMonitor()
	if s.monitor == nil || !s.plugins.Active(xiaomiPlugin) {
		t.Fatal("fixture did not activate the selected device module")
	}
	wantMappings, err := store.keys(profileA)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.selectDeviceProfile(profileB.ID); err == nil {
		t.Fatal("selected another device while the old module remained faulted")
	}
	if !s.deviceNeedsRestart || s.monitor != monitor || !s.plugins.Active(xiaomiPlugin) || monitor.closeCalls != 1 {
		t.Fatalf("failed native cleanup state: restart=%t monitor=%v active=%t closes=%d", s.deviceNeedsRestart, s.monitor, s.plugins.Active(xiaomiPlugin), monitor.closeCalls)
	}
	if s.selectedProfile != profileA.ID || !s.deviceNeedsRestart || !s.plugins.Active(xiaomiPlugin) || monitor.closeCalls != 1 || !reflect.DeepEqual(s.coordinator.mappings, wantMappings) {
		t.Fatalf("switch crossed faulted plugin boundary: selected=%q restart=%t active=%t closes=%d mappings=%v", s.selectedProfile, s.deviceNeedsRestart, s.plugins.Active(xiaomiPlugin), monitor.closeCalls, s.coordinator.mappings)
	}
	if err := s.selectDeviceProfile(profileB.ID); err == nil {
		t.Fatal("second profile switch attempt bypassed the faulted module")
	}
	if s.selectedProfile != profileA.ID || !s.deviceNeedsRestart || !s.plugins.Active(xiaomiPlugin) || monitor.closeCalls != 1 || !reflect.DeepEqual(s.coordinator.mappings, wantMappings) {
		t.Fatalf("second attempt changed faulted profile state: selected=%q restart=%t active=%t closes=%d mappings=%v", s.selectedProfile, s.deviceNeedsRestart, s.plugins.Active(xiaomiPlugin), monitor.closeCalls, s.coordinator.mappings)
	}
	if s.coordinator.report().Pressed != 0 {
		t.Fatal("fixture unexpectedly left a physical press active")
	}
	if err := s.plugins.Close(); err == nil {
		t.Fatal("runtime forgot the monitor cleanup failure")
	}
	if monitor.closeCalls != 1 {
		t.Fatal("faulted cleanup was invoked twice")
	}
}

func TestOutputDetachDoesNotRetainSuccessfullyClosedSink(t *testing.T) {
	d := newDeviceCoordinator()
	sink := &closeableProfileSink{failSubmitAt: 2}
	if err := d.attachOutput(sink); err != nil {
		t.Fatal(err)
	}
	d.mappings = map[string][]bridge.Key{"button-a": {"Up"}}
	device, _ := bridge.NewDevice("profile-test", 1)
	press, _ := bridge.NewPressToken(device, "button-a", 1)
	if _, err := d.accept(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: device, Edges: []xiaomi.Edge{{Kind: xiaomi.EdgeDown, Press: press}}}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sink.Held(), []bridge.Key{"Up"}) {
		t.Fatalf("fixture did not hold mapped key: %v", sink.Held())
	}

	if err := d.detachOutput(); err != nil { // The scope-release submission fails; ReleaseAll and Close succeed.
		t.Fatalf("successful native cleanup returned an error: %v", err)
	}
	if !sink.closed || len(sink.Held()) != 0 {
		t.Fatalf("sink cleanup did not succeed: closed=%t held=%v", sink.closed, sink.Held())
	}
	if d.sink != nil {
		t.Fatal("coordinator retained a closed sink after successful Close")
	}
	if d.report().OutputFailureID == 0 || d.report().Pressed != 1 {
		t.Fatalf("output recovery unlocked before the physical up: %+v", d.report())
	}
	if !reflect.DeepEqual(sink.releases, []bridge.Effect{bridge.KeyUpEffect{Key: "Up"}}) {
		t.Fatalf("Close did not clean the held key: %v", sink.releases)
	}

	oldSubmissionCount := len(sink.submitted)
	if _, err := d.accept(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: device, Edges: []xiaomi.Edge{{Kind: xiaomi.EdgeUp, Press: press}}}); err != nil {
		t.Fatal(err)
	}
	if d.report().OutputFailureID != 0 || d.report().Pressed != 0 || len(sink.submitted) != oldSubmissionCount || sink.submitCalls != 2 {
		t.Fatalf("physical release was replayed or did not unlock recovery: report=%+v submits=%v calls=%d", d.report(), sink.submitted, sink.submitCalls)
	}

	newSink := &closeableProfileSink{}
	if err := d.attachOutput(newSink); err != nil {
		t.Fatal(err)
	}
	fresh, _ := bridge.NewPressToken(device, "button-a", 2)
	if _, err := d.accept(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: device, Edges: []xiaomi.Edge{{Kind: xiaomi.EdgeDown, Press: fresh}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.accept(xiaomi.MonitorEvent{State: xiaomi.MonitorStateConnected, Device: device, Edges: []xiaomi.Edge{{Kind: xiaomi.EdgeUp, Press: fresh}}}); err != nil {
		t.Fatal(err)
	}
	wantNewSubmissions := []bridge.Effect{bridge.KeyDownEffect{Key: "Up"}, bridge.KeyUpEffect{Key: "Up"}}
	if !reflect.DeepEqual(newSink.submitted, wantNewSubmissions) || d.report().OutputFailureID != 0 || d.report().Pressed != 0 || d.report().SubmittedEffects != 3 {
		t.Fatalf("fresh press did not work after recovery: submitted=%v report=%+v", newSink.submitted, d.report())
	}
}
