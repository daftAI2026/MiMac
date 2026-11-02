package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
)

// These fakes exercise saved mapping reconciliation and Core's release path
// without opening the native key mapping service or key output adapter.
type remoteKeysRecoveryReservation struct {
	label      string
	updates    [][]string
	closeCalls int
	updateErr  error
	closeErr   error
	events     *[]string
}

func (r *remoteKeysRecoveryReservation) UpdateKeys(buttonIDs []string) error {
	r.updates = append(r.updates, append([]string(nil), buttonIDs...))
	if r.events != nil {
		*r.events = append(*r.events, "update:"+r.label)
	}
	return r.updateErr
}

func (r *remoteKeysRecoveryReservation) Close() error {
	r.closeCalls++
	if r.events != nil {
		*r.events = append(*r.events, "reservation-close:"+r.label)
	}
	return r.closeErr
}

type remoteKeysRecoverySink struct {
	held           []bridge.Key
	events         *[]string
	failKeyUp      bool
	failReleaseAll bool
}

func (s *remoteKeysRecoverySink) Submit(effects []bridge.Effect) error {
	for _, effect := range effects {
		switch e := effect.(type) {
		case bridge.KeyDownEffect:
			*s.events = append(*s.events, "down:"+string(e.Key))
			s.held = append(s.held, e.Key)
		case bridge.KeyUpEffect:
			*s.events = append(*s.events, "up:"+string(e.Key))
			if s.failKeyUp {
				return errors.New("fake key-up failed")
			}
			s.removeHeld(e.Key)
		default:
			return fmt.Errorf("unexpected fake key effect %T", effect)
		}
	}
	return nil
}

func (s *remoteKeysRecoverySink) ReleaseAll() error {
	*s.events = append(*s.events, "release-all")
	if s.failReleaseAll {
		return errors.New("fake release-all failed")
	}
	for i := len(s.held) - 1; i >= 0; i-- {
		*s.events = append(*s.events, "up:"+string(s.held[i]))
	}
	s.held = nil
	return nil
}

func (s *remoteKeysRecoverySink) Close() error {
	*s.events = append(*s.events, "sink-close")
	if err := s.ReleaseAll(); err != nil {
		return err
	}
	return nil
}

func (s *remoteKeysRecoverySink) Held() []bridge.Key {
	return append([]bridge.Key(nil), s.held...)
}

func (s *remoteKeysRecoverySink) removeHeld(key bridge.Key) {
	for i := len(s.held) - 1; i >= 0; i-- {
		if s.held[i] == key {
			s.held = append(s.held[:i], s.held[i+1:]...)
			return
		}
	}
}

func newSavedMultiButtonRemoteRecovery(t *testing.T, connectAt ...int) (*shell, *powerTestMonitorFactory, chan func(), []string, *[]*remoteKeysRecoveryReservation) {
	t.Helper()
	s, factory, queue, _ := newPowerPermissionRecoveryShell(t, connectAt...)

	upButton, downButton := "hid:0007:0052", "hid:0007:0051"
	profile := emptyProfile()
	profile.Profiles[s.activeProfile().ID] = map[string]string{upButton: "A", downButton: "B"}
	s.mappingPath = filepath.Join(t.TempDir(), "mappings.json")
	if err := saveProfile(s.mappingPath, profile); err != nil {
		t.Fatal(err)
	}
	loaded, err := readProfile(s.mappingPath)
	if err != nil {
		t.Fatal(err)
	}
	s.profile = loaded

	wantSources := []string{downButton, upButton} // customRemoteKeys sorts source IDs.
	owners := make([]*remoteKeysRecoveryReservation, 0, 3)
	s.remoteState.reserve = func(string) (remoteVoiceReservation, error) {
		generation := s.coordinator.core.Snapshot().Generation
		owner := &remoteKeysRecoveryReservation{label: fmt.Sprintf("generation-%d", generation)}
		owners = append(owners, owner)
		return owner, nil
	}
	return s, factory, queue, wantSources, &owners
}

func TestSavedMultiButtonReservationSourceSetRebuildsAcrossSleepWake(t *testing.T) {
	s, factory, queue, wantSources, owners := newSavedMultiButtonRemoteRecovery(t, 1, 2)
	enablePowerTestRemote(t, s)
	if len(*owners) != 1 || !reflect.DeepEqual((*owners)[0].updates, [][]string{wantSources}) {
		t.Fatalf("initial saved source set = %#v, want %#v", *owners, [][]string{wantSources})
	}
	oldOwner := (*owners)[0]

	s.setPowerSuspended(true)
	pumpPowerDispatches(t, queue, func() bool { return !s.remoteBusy() && !s.remoteEnabled() })
	if oldOwner.closeCalls != 1 || s.remoteState.reservation != nil {
		t.Fatalf("sleep did not retire the old source-set owner: closes=%d current=%v", oldOwner.closeCalls, s.remoteState.reservation != nil)
	}

	s.setPowerSuspended(false)
	if len(*owners) != 2 {
		t.Fatalf("wake reservations = %d, want a fresh generation owner", len(*owners))
	}
	newOwner := (*owners)[1]
	if !reflect.DeepEqual(newOwner.updates, [][]string{wantSources}) || s.remoteState.reservation != newOwner ||
		s.remoteState.reservationGeneration != 2 || !s.remoteEnabled() || factory.starts != 2 {
		t.Fatalf("wake did not rebuild both saved sources on the new generation: updates=%#v owner=%v generation=%d enabled=%v starts=%d",
			newOwner.updates, s.remoteState.reservation == newOwner, s.remoteState.reservationGeneration, s.remoteEnabled(), factory.starts)
	}
}

func TestSavedMultiButtonReservationSourceSetRebuildsAcrossDisconnectReconnect(t *testing.T) {
	s, factory, queue, wantSources, owners := newSavedMultiButtonRemoteRecovery(t, 1)
	enablePowerTestRemote(t, s)
	if len(*owners) != 1 || !reflect.DeepEqual((*owners)[0].updates, [][]string{wantSources}) {
		t.Fatalf("initial saved source set = %#v, want %#v", *owners, [][]string{wantSources})
	}
	oldOwner := (*owners)[0]
	factory.monitors[0].emit(remoteKeysRecoveryConnectionEvent(t, xiaomi.MonitorStateDisconnected, 1))
	remoteDispatch(t, queue)()
	if oldOwner.closeCalls != 1 || s.remoteState.reservation != nil {
		t.Fatalf("disconnect did not retire the old source-set owner: closes=%d current=%v", oldOwner.closeCalls, s.remoteState.reservation != nil)
	}

	factory.monitors[0].emit(remoteKeysRecoveryConnectionEvent(t, xiaomi.MonitorStateConnected, 2))
	remoteDispatch(t, queue)()
	if len(*owners) != 2 {
		t.Fatalf("reconnect reservations = %d, want a fresh generation owner", len(*owners))
	}
	newOwner := (*owners)[1]
	if !reflect.DeepEqual(newOwner.updates, [][]string{wantSources}) || s.remoteState.reservation != newOwner ||
		s.remoteState.reservationGeneration != 2 || s.coordinator.core.Snapshot().Generation != 2 {
		t.Fatalf("reconnect did not rebuild both saved sources: updates=%#v owner=%v reservation generation=%d core=%d",
			newOwner.updates, s.remoteState.reservation == newOwner, s.remoteState.reservationGeneration, s.coordinator.core.Snapshot().Generation)
	}
}

func remoteKeysRecoveryConnectionEvent(t *testing.T, state string, generation uint64) xiaomi.MonitorEvent {
	t.Helper()
	device, err := bridge.NewDevice("power-test-remote", generation)
	if err != nil {
		t.Fatal(err)
	}
	return xiaomi.MonitorEvent{State: state, Device: device}
}

func newRemoteKeysRecoveryStop(t *testing.T, sink *remoteKeysRecoverySink, owner *remoteKeysRecoveryReservation) (func() error, *shell) {
	t.Helper()
	coordinator := newDeviceCoordinator()
	coordinator.mappings = map[string][]bridge.Key{"button-up": {"A"}, "button-down": {"B"}}
	if err := coordinator.attachOutput(sink); err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice("remote-keys-stop", 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.accept(devices.InputEvent{State: devices.InputStateConnected, Device: device}); err != nil {
		t.Fatal(err)
	}
	for index, buttonID := range []string{"button-up", "button-down"} {
		press, err := bridge.NewPressToken(device, buttonID, uint64(index+1))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := coordinator.accept(devices.InputEvent{
			State:  devices.InputStateConnected,
			Device: device,
			Edges:  []devices.Edge{{Kind: devices.EdgeDown, Press: press}},
		}); err != nil {
			t.Fatal(err)
		}
	}

	s := &shell{
		coordinator: coordinator,
		monitor:     &remoteTestMonitor{},
		remoteState: remoteState{
			reservation:           owner,
			reservationGeneration: 7,
			reserve: func(string) (remoteVoiceReservation, error) {
				return owner, nil
			},
			snapshotPath: filepath.Join(t.TempDir(), "voice-mapping-recovery.plist"),
		},
	}
	stop, err := s.remoteStart()
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.Held()) != 2 {
		t.Fatalf("fixture held keys = %v, want both mapped keys", sink.Held())
	}
	return stop, s
}

func TestRemoteStopReleasesHeldSyntheticKeysBeforeClosingReservation(t *testing.T) {
	events := []string{}
	owner := &remoteKeysRecoveryReservation{label: "stop", events: &events}
	sink := &remoteKeysRecoverySink{events: &events}
	stop, s := newRemoteKeysRecoveryStop(t, sink, owner)
	if !reflect.DeepEqual(events, []string{"down:A", "down:B"}) || !reflect.DeepEqual(sink.Held(), []bridge.Key{"A", "B"}) {
		t.Fatalf("stop fixture did not submit both synthetic downs first: events=%v held=%v", events, sink.Held())
	}

	if err := stop(); err != nil {
		t.Fatalf("remote stop: %v", err)
	}
	closeIndex := -1
	upCount := 0
	for index, event := range events {
		if len(event) >= len("up:") && event[:len("up:")] == "up:" {
			upCount++
		}
		if event == "reservation-close:stop" {
			closeIndex = index
		}
	}
	if upCount != 2 || closeIndex < 0 || closeIndex < 2 || len(sink.Held()) != 0 ||
		s.remoteState.reservation != nil || owner.closeCalls != 1 {
		t.Fatalf("stop order/ownership: events=%v held=%v reservation=%v closeCalls=%d, want two key-ups before close",
			events, sink.Held(), s.remoteState.reservation != nil, owner.closeCalls)
	}
	for index, event := range events {
		if event == "reservation-close:stop" {
			continue
		}
		if len(event) >= len("up:") && event[:len("up:")] == "up:" && index >= closeIndex {
			t.Fatalf("reservation closed before a synthesized key release: %v", events)
		}
	}
}

func TestRemoteStopRetainsReservationWhenSyntheticKeyReleaseFails(t *testing.T) {
	events := []string{}
	owner := &remoteKeysRecoveryReservation{label: "release-failure", events: &events}
	sink := &remoteKeysRecoverySink{events: &events, failKeyUp: true, failReleaseAll: true}
	stop, s := newRemoteKeysRecoveryStop(t, sink, owner)

	if err := stop(); err == nil {
		t.Fatal("remote stop hid key release failure")
	}
	if s.remoteState.reservation != owner || owner.closeCalls != 0 || len(sink.Held()) != 2 {
		t.Fatalf("failed release dropped a live owner: reservation=%v closes=%d held=%v events=%v",
			s.remoteState.reservation == owner, owner.closeCalls, sink.Held(), events)
	}
}

func TestRemoteStopRetainsReservationWhenReservationCloseFails(t *testing.T) {
	events := []string{}
	closeErr := errors.New("fake reservation close failed")
	owner := &remoteKeysRecoveryReservation{label: "close-failure", events: &events, closeErr: closeErr}
	sink := &remoteKeysRecoverySink{events: &events}
	stop, s := newRemoteKeysRecoveryStop(t, sink, owner)

	if err := stop(); !errors.Is(err, closeErr) {
		t.Fatalf("remote stop error = %v, want reservation close failure", err)
	}
	if s.remoteState.reservation != owner || owner.closeCalls != 1 || len(sink.Held()) != 0 {
		t.Fatalf("failed reservation close lost its owner or skipped key release: reservation=%v closes=%d held=%v events=%v",
			s.remoteState.reservation == owner, owner.closeCalls, sink.Held(), events)
	}
}
