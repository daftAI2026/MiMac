package main

import (
	"context"
	"sync"
	"testing"

	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

// This fixture keeps the real host/Core/plugin paths while replacing HID,
// capture, transcript delivery, and key output with fakes.
type hostRecoveryFixture struct {
	s         *shell
	factory   *powerTestMonitorFactory
	main      chan func()
	monitor   *remoteTestMonitor
	keys      *routedEffectsKeySink
	source    *hostPCMSource
	sink      *hostTranscriptTestSink
	delivered *transcriptDeliveryRecorder
	press     bridge.PressToken
	token     bridge.SessionToken
	device    bridge.Device
}

func newHostRecoveryFixture(t *testing.T) *hostRecoveryFixture {
	t.Helper()
	s, factory, main := newPowerTestShell(t, 1, 2)
	enablePowerTestRemote(t, s)
	s.audioPost = func(fn func()) { main <- fn }
	s.coordinator.mappings = nil // Avoid the native output-permission path.
	keys := &routedEffectsKeySink{}
	if err := s.coordinator.attachOutput(keys); err != nil {
		t.Fatal(err)
	}

	source, sink := newHostPCMSource(), newHostTranscriptTestSink(nil)
	delivered := &transcriptDeliveryRecorder{}
	s.audioResources = func(context.Context, bridge.SessionToken) (audiosession.Resources, error) {
		return audiosession.Resources{Source: source, Sink: sink, SampleRate: 16000}, nil
	}
	s.audioResourcesDeliver = delivered.deliver
	if err := s.plugins.Enable(audioSessionPlugin); err != nil {
		t.Fatalf("enable fake audio-session plugin: %v", err)
	}

	plan := voiceAudioTestPlan(t, bridge.ActionTap, bridge.RequestToggle, bridge.RequestNone, "RightOption")
	s.coordinator.voiceButton = "voice-test"
	s.coordinator.resolveLocalVoice = func() (bridge.Binding, bool, error) {
		return bridge.Binding{}, false, nil
	}
	s.coordinator.resolveVoiceAction = func() (bridge.ActionPlan, error) { return plan, nil }
	monitor := factory.monitors[0]
	device, err := bridge.NewDevice("power-test-remote", 1)
	if err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, s.coordinator.voiceButton, 1)
	if err != nil {
		t.Fatal(err)
	}
	return &hostRecoveryFixture{
		s: s, factory: factory, main: main, monitor: monitor, keys: keys,
		source: source, sink: sink, delivered: delivered, press: press, device: device,
	}
}

func (f *hostRecoveryFixture) beginCapture(t *testing.T) {
	t.Helper()
	f.monitor.emit(xiaomi.MonitorEvent{
		State: xiaomi.MonitorStateConnected, Device: f.device,
		Edges: []xiaomi.Edge{{Kind: xiaomi.EdgeDown, Press: f.press}},
	})
	snapshot := f.s.coordinator.core.Snapshot()
	if !snapshot.HasSession || snapshot.Session.Phase != bridge.PhasePreparing {
		t.Fatalf("physical host press did not prepare voice session: %+v", snapshot)
	}
	runHostTask(t, f.main) // The production SourcePrepared callback opens the Core gate.
	waitHostSignal(t, f.source.reading)
	snapshot = f.s.coordinator.core.Snapshot()
	if !snapshot.HasSession || snapshot.Session.Phase != bridge.PhaseCapturing {
		t.Fatalf("fake source did not reach capture through the host callback: %+v", snapshot)
	}
	f.token = snapshot.Session.Token
}

func (f *hostRecoveryFixture) targetTapCount() int {
	return countPowerTargetTapsFromBatches(f.keys.batches)
}

func countPowerTargetTapsFromBatches(batches [][]bridge.Effect) int {
	count := 0
	for _, batch := range batches {
		count += countPowerTargetTaps(batch)
	}
	return count
}

func installDelayedAudioClose(t *testing.T, s *shell) (<-chan struct{}, func()) {
	t.Helper()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	t.Cleanup(open)
	s.remoteState.closeAudio = func(ctx context.Context, host *audioSessionHost) error {
		started <- struct{}{}
		<-release
		return host.close(ctx)
	}
	return started, open
}

func TestLockOnlyRestoresAfterActiveHostVoiceCleanup(t *testing.T) {
	f := newHostRecoveryFixture(t)
	defer func() {
		if f.s.plugins != nil {
			if err := f.s.plugins.Close(); err != nil {
				t.Errorf("close fake plugins: %v", err)
			}
		}
	}()
	f.beginCapture(t)
	if got := f.targetTapCount(); got != 1 {
		t.Fatalf("voice start target taps=%d, want one positive start", got)
	}
	oldToken := f.token
	oldOwner := f.factory.reservations[0]
	started, release := installDelayedAudioClose(t, f.s)
	host := f.s.audioSession
	if host == nil {
		t.Fatal("active fake capture has no host session owner")
	}

	f.s.setPowerLocked(true) // Exercise the lock-only host lifecycle.
	waitHostSignal(t, started)
	if f.s.powerState.sleeping || !f.s.powerState.locked || !f.s.powerInputBlocked() {
		t.Fatalf("lock-only pause state = %+v", f.s.powerState)
	}
	if snapshot := f.s.coordinator.core.Snapshot(); snapshot.HasSession || len(snapshot.HeldKeys) != 0 {
		t.Fatalf("locking left active voice state in Core: %+v", snapshot)
	}
	if f.s.remoteState.reservation != oldOwner || oldOwner.closeCalls != 0 {
		t.Fatal("old remote lease was retired before active host cleanup finished")
	}
	waitHostSignal(t, f.sink.closed)
	if f.sink.drains.Load() != 0 || f.sink.transcriptCalls.Load() != 0 {
		t.Fatalf("lock drained a cancelled capture: drains=%d transcripts=%d", f.sink.drains.Load(), f.sink.transcriptCalls.Load())
	}

	f.s.setPowerLocked(false)
	if f.s.powerState.sleeping || f.s.powerState.locked || !f.s.powerInputBlocked() || f.factory.starts != 1 {
		t.Fatalf("unlock reopened before cleanup: power=%+v blocked=%v starts=%d",
			f.s.powerState, f.s.powerInputBlocked(), f.factory.starts)
	}
	release()
	pumpPowerDispatches(t, f.main, func() bool {
		return !f.s.powerInputBlocked() && f.s.remoteEnabled() && f.s.remoteState.reservationGeneration == 2
	})
	if f.factory.starts != 2 || f.factory.reservedFor[len(f.factory.reservedFor)-1] != 2 ||
		f.s.audioSession != nil || f.s.coordinator.core.Snapshot().HasSession || oldOwner.closeCalls != 1 {
		t.Fatalf("unlock did not restore a fresh owner after host cleanup: starts=%d reserved=%v audio=%v core=%+v old closes=%d",
			f.factory.starts, f.factory.reservedFor, f.s.audioSession != nil, f.s.coordinator.core.Snapshot(), oldOwner.closeCalls)
	}
	late, err := f.s.coordinator.core.Step(bridge.SourceDrained(oldToken))
	if err != nil {
		t.Fatal(err)
	}
	if countPowerTargetTapsFromBatches([][]bridge.Effect{late}) != 0 || f.targetTapCount() != 1 || f.s.coordinator.core.Snapshot().HasSession {
		t.Fatalf("old completion revived toggle after unlock: effects=%#v taps=%d snapshot=%+v", late, f.targetTapCount(), f.s.coordinator.core.Snapshot())
	}
}

func TestHostDisconnectFencesQueuedFinalTranscriptBeforeLeaseHandoff(t *testing.T) {
	f := newHostRecoveryFixture(t)
	defer func() {
		if f.s.remoteState.reservation != nil {
			_ = f.s.remoteState.reservation.Close()
			f.s.remoteState.reservation = nil
		}
		if f.s.plugins != nil {
			if err := f.s.plugins.Close(); err != nil {
				t.Errorf("close fake plugins: %v", err)
			}
		}
	}()
	f.beginCapture(t)
	f.monitor.emit(xiaomi.MonitorEvent{
		State: xiaomi.MonitorStateConnected, Device: f.device,
		Edges: []xiaomi.Edge{{Kind: xiaomi.EdgeUp, Press: f.press}},
	})
	waitHostSignal(t, f.sink.drainStarted)
	lateFinal := runAndReturnHostTask(t, f.main) // Final text is ready but not applied on the host thread.
	if f.sink.transcriptCalls.Load() != 1 || f.targetTapCount() != 1 {
		t.Fatalf("test did not queue a final result before the target toggle commit: transcripts=%d taps=%d",
			f.sink.transcriptCalls.Load(), f.targetTapCount())
	}
	oldOwner := f.factory.reservations[0]
	started, release := installDelayedAudioClose(t, f.s)
	f.monitor.emit(remoteKeysRecoveryConnectionEvent(t, xiaomi.MonitorStateDisconnected, 1))
	waitHostSignal(t, started)
	if snapshot := f.s.coordinator.core.Snapshot(); snapshot.HasSession || f.s.remoteState.reservation != oldOwner || oldOwner.closeCalls != 0 {
		t.Fatalf("disconnect failed to retire Core while retaining the old owner through cleanup: core=%+v owner=%v closes=%d",
			snapshot, f.s.remoteState.reservation == oldOwner, oldOwner.closeCalls)
	}

	// The physical reconnect can update Core while the generation-1 audio owner
	// is still closing, but must not reserve a generation-2 native owner yet.
	f.monitor.emit(remoteKeysRecoveryConnectionEvent(t, xiaomi.MonitorStateConnected, 2))
	if snapshot := f.s.coordinator.core.Snapshot(); !snapshot.Connected || snapshot.Generation != 2 || snapshot.HasSession {
		t.Fatalf("early reconnect Core state = %+v", snapshot)
	}
	if len(f.factory.reservations) != 1 || f.s.remoteState.reservation != oldOwner || oldOwner.closeCalls != 0 {
		t.Fatalf("new lease was acquired before old cleanup: reserves=%d owner=%v closes=%d",
			len(f.factory.reservations), f.s.remoteState.reservation == oldOwner, oldOwner.closeCalls)
	}

	lateFinal()
	if got := f.delivered.entries(); len(got) != 0 || f.targetTapCount() != 1 {
		t.Fatalf("queued final text committed after host disconnect: delivered=%#v target taps=%d", got, f.targetTapCount())
	}
	waitHostSignal(t, f.sink.closed)
	if f.sink.transcriptCalls.Load() != 1 || f.s.coordinator.core.Snapshot().HasSession {
		t.Fatalf("late completion changed retired audio state: transcripts=%d core=%+v", f.sink.transcriptCalls.Load(), f.s.coordinator.core.Snapshot())
	}

	release()
	remoteDispatch(t, f.main)()
	if len(f.factory.reservations) != 2 || f.s.remoteState.reservation != f.factory.reservations[1] ||
		f.s.remoteState.reservationGeneration != 2 || !f.s.remoteHasReservation() || oldOwner.closeCalls != 1 ||
		f.s.audioSession != nil || f.targetTapCount() != 1 || len(f.delivered.entries()) != 0 {
		t.Fatalf("cleanup did not hand off safely to gen 2: owners=%d current=%v generation=%d audio=%v old closes=%d taps=%d delivered=%#v",
			len(f.factory.reservations), f.s.remoteState.reservation == f.factory.reservations[1], f.s.remoteState.reservationGeneration,
			f.s.audioSession != nil, oldOwner.closeCalls, f.targetTapCount(), f.delivered.entries())
	}
}
