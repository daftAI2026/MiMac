package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/bridge"
)

func TestSavedMultiButtonReconnectDuringAudioCleanupUsesOnlyNewGeneration(t *testing.T) {
	s, factory, queue, wantSources, owners := newSavedMultiButtonRemoteRecovery(t, 1)
	keys, err := s.profile.keys(s.activeProfile())
	if err != nil {
		t.Fatal(err)
	}
	s.coordinator.mappings = keys
	if err := s.plugins.Enable(remotePlugin); err != nil {
		t.Fatalf("enable remote plugin: %v", err)
	}
	if len(*owners) != 1 || !reflect.DeepEqual((*owners)[0].updates, [][]string{wantSources}) {
		t.Fatalf("initial saved source set = %#v, want %#v", *owners, [][]string{wantSources})
	}
	oldOwner := (*owners)[0]

	if err := s.plugins.Enable(outputPlugin); err != nil {
		t.Fatalf("enable fake output plugin: %v", err)
	}
	oldSink, ok := s.output.(*fakeKeySink)
	if !ok || oldSink == nil {
		t.Fatalf("output sink = %T, want the fake key sink", s.output)
	}

	oldDeviceEvent := remoteKeysRecoveryConnectionEvent(t, xiaomi.MonitorStateConnected, 1)
	oldDevice := oldDeviceEvent.Device
	oldPress, err := bridge.NewPressToken(oldDevice, "hid:0007:0052", 1)
	if err != nil {
		t.Fatal(err)
	}
	emitEdge := func(device bridge.Device, press bridge.PressToken, kind xiaomi.EdgeKind) {
		t.Helper()
		factory.monitors[0].emit(xiaomi.MonitorEvent{
			State:  xiaomi.MonitorStateConnected,
			Device: device,
			Edges:  []xiaomi.Edge{{Kind: kind, Press: press}},
		})
	}
	emitEdge(oldDevice, oldPress, xiaomi.EdgeDown)
	emitEdge(oldDevice, oldPress, xiaomi.EdgeUp)
	if len(oldSink.effects) != 2 {
		t.Fatalf("positive fake output effects = %#v, want mapped down and up", oldSink.effects)
	}
	if down, ok := oldSink.effects[0].(bridge.KeyDownEffect); !ok || down.Key != "A" {
		t.Fatalf("positive key-down = %#v, want A", oldSink.effects[0])
	}
	if up, ok := oldSink.effects[1].(bridge.KeyUpEffect); !ok || up.Key != "A" {
		t.Fatalf("positive key-up = %#v, want A", oldSink.effects[1])
	}
	baselineOutputEffects := len(oldSink.effects)

	cleanupStarted := make(chan struct{}, 1)
	finishAudioCleanup := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-finishAudioCleanup:
		default:
			close(finishAudioCleanup)
		}
	})
	host := &audioSessionHost{cancel: func() {}}
	s.audioSession = host
	s.remoteState.closeAudio = func(_ context.Context, got *audioSessionHost) error {
		if got != host {
			t.Errorf("audio cleanup host = %p, want %p", got, host)
		}
		cleanupStarted <- struct{}{}
		<-finishAudioCleanup
		return nil
	}

	factory.monitors[0].emit(remoteKeysRecoveryConnectionEvent(t, xiaomi.MonitorStateDisconnected, 1))
	select {
	case <-cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("disconnect did not begin the delayed audio cleanup")
	}
	if !s.remoteState.cleanupInFlight || s.remoteState.reservation != oldOwner || oldOwner.closeCalls != 0 {
		t.Fatalf("pending cleanup lost the old owner: cleanup=%v owner=%v closes=%d",
			s.remoteState.cleanupInFlight, s.remoteState.reservation == oldOwner, oldOwner.closeCalls)
	}

	// Reconnect reaches the production monitor callback before the old audio
	// owner finishes. It must update Core without racing a new native reserve.
	newDeviceEvent := remoteKeysRecoveryConnectionEvent(t, xiaomi.MonitorStateConnected, 2)
	newDevice := newDeviceEvent.Device
	factory.monitors[0].emit(newDeviceEvent)
	if snapshot := s.coordinator.core.Snapshot(); !snapshot.Connected || snapshot.Generation != 2 {
		t.Fatalf("early reconnect did not enter Core: %+v", snapshot)
	}
	if len(*owners) != 1 || s.remoteState.reservation != oldOwner || oldOwner.closeCalls != 0 || s.remoteState.reserveQueued != 0 {
		t.Fatalf("pending cleanup changed lease ownership: reserves=%d owner=%v closes=%d queued=%d",
			len(*owners), s.remoteState.reservation == oldOwner, oldOwner.closeCalls, s.remoteState.reserveQueued)
	}
	select {
	case <-queue:
		t.Fatal("reconnect queued a reservation while old audio cleanup was pending")
	default:
	}

	newPress, err := bridge.NewPressToken(newDevice, "hid:0007:0052", 2)
	if err != nil {
		t.Fatal(err)
	}
	emitEdge(newDevice, newPress, xiaomi.EdgeDown)
	emitEdge(newDevice, newPress, xiaomi.EdgeUp)
	if len(oldSink.effects) != baselineOutputEffects {
		t.Fatalf("new-generation key produced output before cleanup finished: effects=%#v", oldSink.effects[baselineOutputEffects:])
	}
	if got := s.coordinator.report().Pressed; got != 0 {
		t.Fatalf("new-generation observed key remained pressed during cleanup: %d", got)
	}
	if len(*owners) != 1 || oldOwner.closeCalls != 0 {
		t.Fatalf("pending cleanup acquired or closed a lease early: reserves=%d old closes=%d", len(*owners), oldOwner.closeCalls)
	}

	// The persisted profile still holds both mappings and drives lease
	// reconciliation. Clear only Core's test projection so completion's output
	// refresh cannot query real Post Event permission in this fake-only test.
	s.coordinator.mappings = nil
	close(finishAudioCleanup)
	oldCleanupCompletion := remoteDispatch(t, queue)
	oldCleanupCompletion() // Finish generation 1 after Core already accepted generation 2.

	if len(*owners) != 2 {
		t.Fatalf("cleanup completion reserve count = %d, want exactly one fresh owner", len(*owners))
	}
	newOwner := (*owners)[1]
	if newOwner == oldOwner || !reflect.DeepEqual(newOwner.updates, [][]string{wantSources}) ||
		s.remoteState.reservation != newOwner || s.remoteState.reservationGeneration != 2 || !s.remoteHasReservation() {
		t.Fatalf("completion did not rebuild the saved source set on generation 2: updates=%#v current=%v ownerGeneration=%d core=%+v",
			newOwner.updates, s.remoteState.reservation == newOwner, s.remoteState.reservationGeneration, s.coordinator.core.Snapshot())
	}
	if oldOwner.closeCalls != 1 || s.coordinator.core.Snapshot().Generation != 2 {
		t.Fatalf("old completion did not retire only generation 1: old closes=%d core=%+v", oldOwner.closeCalls, s.coordinator.core.Snapshot())
	}
	oldCleanupCompletion() // A delayed duplicate from generation 1 must leave generation 2 owned.
	if s.remoteState.reservation != newOwner || s.remoteState.reservationGeneration != 2 ||
		oldOwner.closeCalls != 1 || newOwner.closeCalls != 0 || len(*owners) != 2 ||
		!reflect.DeepEqual(newOwner.updates, [][]string{wantSources}) {
		t.Fatalf("stale cleanup completion changed the generation-2 owner: current=%v generation=%d old/new closes=%d/%d reserves=%d updates=%#v",
			s.remoteState.reservation == newOwner, s.remoteState.reservationGeneration,
			oldOwner.closeCalls, newOwner.closeCalls, len(*owners), newOwner.updates)
	}
	s.coordinator.mappings = keys

	// A late generation-1 callback must not retire or replace the new owner.
	stalePress, err := bridge.NewPressToken(oldDevice, "hid:0007:0052", 3)
	if err != nil {
		t.Fatal(err)
	}
	emitEdge(oldDevice, stalePress, xiaomi.EdgeDown)
	factory.monitors[0].emit(remoteKeysRecoveryConnectionEvent(t, xiaomi.MonitorStateDisconnected, 1))
	if s.remoteState.reservation != newOwner || s.remoteState.reservationGeneration != 2 ||
		oldOwner.closeCalls != 1 || newOwner.closeCalls != 0 || len(*owners) != 2 ||
		s.coordinator.core.Snapshot().Generation != 2 || s.coordinator.report().Pressed != 0 {
		t.Fatalf("retired generation affected the new owner: current=%v generation=%d old/new closes=%d/%d reserves=%d core=%+v pressed=%d",
			s.remoteState.reservation == newOwner, s.remoteState.reservationGeneration,
			oldOwner.closeCalls, newOwner.closeCalls, len(*owners), s.coordinator.core.Snapshot(), s.coordinator.report().Pressed)
	}
}
