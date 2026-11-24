package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
)

func TestLiveAudioHostRecoversFailedIdleRouteOnce(t *testing.T) {
	s, queue := liveHostFixture(t, nil)
	first := newLiveAudioConnectionTestRoute()
	second := newLiveAudioConnectionTestRoute()
	var factoryCalls atomic.Int32
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) {
		if factoryCalls.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	}
	s.refreshLiveAudio()
	failedConnection := s.liveAudio.connection
	if failedConnection == nil {
		t.Fatal("initial route was not created")
	}
	select {
	case <-failedConnection.Done():
	case <-time.After(time.Second):
		t.Fatal("initial route did not finish preparation")
	}
	select {
	case update := <-queue:
		update()
	case <-time.After(time.Second):
		t.Fatal("initial route status was not posted")
	}

	coreGeneration := s.coordinator.core.Snapshot().Generation
	reservation := s.remoteState.reservation
	firstFault := errors.New("prepared source failed")
	failTestRouteSource(first, firstFault)
	select {
	case update := <-queue:
		update()
	case <-time.After(time.Second):
		t.Fatal("source failure status was not posted")
	}

	deadline := time.After(time.Second)
	for s.liveAudio.connection == failedConnection {
		select {
		case update := <-queue:
			update()
		case <-deadline:
			t.Fatalf("failed connection was not replaced: error=%q factories=%d", s.report.AudioError, factoryCalls.Load())
		}
	}
	recoveredConnection := s.liveAudio.connection
	if recoveredConnection == nil {
		t.Fatal("recovery discarded the connection without creating a replacement")
	}
	select {
	case <-recoveredConnection.Done():
	case <-time.After(time.Second):
		t.Fatal("replacement route did not finish preparation")
	}
	if !s.audioResourcesReady() || factoryCalls.Load() != 2 {
		t.Fatalf("recovery readiness=%t factory calls=%d, want ready and exactly two factories", s.audioResourcesReady(), factoryCalls.Load())
	}
	if s.coordinator.core.Snapshot().Generation != coreGeneration || s.remoteState.reservation != reservation {
		t.Fatal("audio recovery changed HID generation or voice-key reservation")
	}

	// A second persistent source fault must stay latched until an explicit retry.
	select {
	case update := <-queue:
		update()
	case <-time.After(time.Second):
		t.Fatal("replacement route status was not posted")
	}
	secondFault := errors.New("replacement source failed")
	failTestRouteSource(second, secondFault)
	deadline = time.After(time.Second)
	for s.report.AudioError != secondFault.Error() {
		select {
		case update := <-queue:
			update()
		case <-deadline:
			t.Fatal("replacement source error was not reported")
		}
	}
	for i := 0; i < 4; i++ {
		s.refreshLiveAudio()
	}
	if s.liveAudio.connection != recoveredConnection || s.liveAudio.retiring || factoryCalls.Load() != 2 {
		t.Fatalf("persistent failure retried automatically: same=%t retiring=%t factories=%d", s.liveAudio.connection == recoveredConnection, s.liveAudio.retiring, factoryCalls.Load())
	}
}

func TestLiveAudioRecoveryRearmsAfterSessionAndBoundsNextFailure(t *testing.T) {
	s, queue := liveHostFixture(t, nil)
	newRoute := func() (*liveAudioConnectionTestRoute, *hostPCMSource) {
		source, sink := newHostPCMSource(), newHostPCMSink()
		route := newLiveAudioConnectionTestRoute()
		route.resources = audiosession.Resources{Source: source, Sink: sink, SampleRate: 16000}
		return route, source
	}
	first, source := newRoute()
	second, _ := newRoute()
	var factoryCalls atomic.Int32
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) {
		if factoryCalls.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	}
	s.refreshLiveAudio()
	connection := s.liveAudio.connection
	if connection == nil {
		t.Fatal("initial route was not created")
	}
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("initial route did not finish preparation")
	}
	select {
	case update := <-queue:
		update()
	case <-time.After(time.Second):
		t.Fatal("initial route status was not posted")
	}

	// Simulate a route that has already consumed its one automatic rebuild.
	// Readiness and SourcePrepared must leave that latch in place.
	s.liveAudio.recoveryAttempted = true
	device, err := bridge.NewDevice("live-host", 1)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bridge.NewBinding(bridge.PolicyAfterDrain, "RightCommand")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.coordinator.attachOutput(&routedEffectsKeySink{}); err != nil {
		t.Fatal(err)
	}
	if err := s.coordinator.step(bridge.Configure(binding)); err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, "voice", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.coordinator.step(bridge.VoiceBegan(press)); err != nil {
		t.Fatal(err)
	}
	pumpLiveHost(t, queue, func() bool {
		snapshot := s.coordinator.core.Snapshot()
		return snapshot.HasSession && snapshot.Session.Phase == bridge.PhaseCapturing
	})
	waitHostSignal(t, source.reading)
	if !s.liveAudio.recoveryAttempted {
		t.Fatal("Ready or SourcePrepared rearmed the recovery latch")
	}
	if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	pumpLiveHost(t, queue, func() bool {
		return !s.coordinator.core.Snapshot().HasSession && !s.liveAudio.recoveryAttempted
	})

	firstFault := errors.New("independent source failure after successful session")
	failTestRouteSource(first, firstFault)
	pumpLiveHost(t, queue, func() bool { return s.liveAudio.connection != connection && factoryCalls.Load() == 2 })
	recovered := s.liveAudio.connection
	if recovered == nil || factoryCalls.Load() != 2 || !s.liveAudio.recoveryAttempted {
		t.Fatalf("post-session fault was not recovered once: connection=%t factories=%d attempted=%t",
			recovered != nil, factoryCalls.Load(), s.liveAudio.recoveryAttempted)
	}
	select {
	case <-recovered.Done():
	case <-time.After(time.Second):
		t.Fatal("rebuilt route did not finish preparation")
	}

	// A persistent error on that replacement cannot immediately consume a
	// third route or form an automatic retry loop before another real session.
	secondFault := errors.New("replacement source remained failed")
	failTestRouteSource(second, secondFault)
	pumpLiveHost(t, queue, func() bool { return s.report.AudioError == secondFault.Error() })
	for i := 0; i < 4; i++ {
		s.refreshLiveAudio()
	}
	if s.liveAudio.connection != recovered || s.liveAudio.retiring || factoryCalls.Load() != 2 {
		t.Fatalf("persistent failure looped after one recovery: same=%t retiring=%t factories=%d",
			s.liveAudio.connection == recovered, s.liveAudio.retiring, factoryCalls.Load())
	}
}

func TestLiveAudioRecoveryDoesNotRetryFailedCleanup(t *testing.T) {
	s, queue := liveHostFixture(t, nil)
	route := newLiveAudioConnectionTestRoute()
	route.closeErrors = []error{errors.New("audio cleanup failed"), nil}
	var factoryCalls atomic.Int32
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) {
		factoryCalls.Add(1)
		return route, nil
	}
	s.refreshLiveAudio()
	connection := s.liveAudio.connection
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("route did not finish preparation")
	}
	select {
	case update := <-queue:
		update()
	case <-time.After(time.Second):
		t.Fatal("initial route status was not posted")
	}
	failTestRouteSource(route, errors.New("prepared source failed"))
	deadline := time.After(time.Second)
	for !s.report.AudioCleanupFailed || s.liveAudio.retiring {
		select {
		case update := <-queue:
			update()
		case <-deadline:
			t.Fatal("source recovery did not report the failed cleanup")
		}
	}
	if s.liveAudio.connection != connection || s.audioSession == nil || factoryCalls.Load() != 1 {
		t.Fatal("failed cleanup lost its original audio owner or opened another route")
	}
	for i := 0; i < 4; i++ {
		s.refreshLiveAudio()
	}
	if _, _, closes := route.calls(); closes != 1 || factoryCalls.Load() != 1 {
		t.Fatalf("failed cleanup was retried automatically: closes=%d factories=%d", closes, factoryCalls.Load())
	}
}

func TestLiveAudioRecoveryLeavesRouteSetupFailureLatched(t *testing.T) {
	s, queue := liveHostFixture(t, nil)
	route := newLiveAudioConnectionTestRoute()
	route.startErr = errors.New("route setup failed")
	var factoryCalls atomic.Int32
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) {
		factoryCalls.Add(1)
		return route, nil
	}
	s.refreshLiveAudio()
	connection := s.liveAudio.connection
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("route setup did not finish")
	}
	select {
	case update := <-queue:
		update()
	case <-time.After(time.Second):
		t.Fatal("route setup status was not posted")
	}
	for i := 0; i < 4; i++ {
		s.refreshLiveAudio()
	}
	if s.liveAudio.connection != connection || s.liveAudio.retiring || factoryCalls.Load() != 1 {
		t.Fatal("unclassified route setup failure triggered source recovery")
	}
}

func TestLiveAudioRecoveryDoesNotRetryBluetoothPermissionRefusal(t *testing.T) {
	s, queue := liveHostFixture(t, nil)
	route := newLiveAudioConnectionTestRoute()
	var factoryCalls atomic.Int32
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) {
		factoryCalls.Add(1)
		return route, nil
	}
	s.refreshLiveAudio()
	connection := s.liveAudio.connection
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("route did not finish preparation")
	}
	select {
	case update := <-queue:
		update()
	case <-time.After(time.Second):
		t.Fatal("initial route status was not posted")
	}

	failTestRouteSource(route, atvv.ErrBluetoothPermission)
	select {
	case update := <-queue:
		update()
	case <-time.After(time.Second):
		t.Fatal("permission refusal status was not posted")
	}
	for i := 0; i < 4; i++ {
		s.refreshLiveAudio()
	}
	if s.liveAudio.connection != connection || s.liveAudio.retiring || s.liveAudio.recoveryAttempted || factoryCalls.Load() != 1 {
		t.Fatalf("permission refusal triggered background recovery: same=%t retiring=%t attempted=%t factories=%d",
			s.liveAudio.connection == connection, s.liveAudio.retiring, s.liveAudio.recoveryAttempted, factoryCalls.Load())
	}
}

func TestLiveAudioRecoveryWaitsForUnknownToggleStateToBeConfirmed(t *testing.T) {
	s, queue := liveHostFixture(t, nil)
	route := newLiveAudioConnectionTestRoute()
	var factoryCalls atomic.Int32
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) {
		factoryCalls.Add(1)
		return route, nil
	}
	s.refreshLiveAudio()
	connection := s.liveAudio.connection
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("route did not finish preparation")
	}
	select {
	case update := <-queue:
		update()
	case <-time.After(time.Second):
		t.Fatal("initial route status was not posted")
	}

	key, err := bridge.NewKey("Control")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.coordinator.core.Step(bridge.OutputFailed(1, key)); err != nil {
		t.Fatal(err)
	}
	snapshot := s.coordinator.core.Snapshot()
	if len(snapshot.HeldKeys) != 0 || len(snapshot.RecoveryKeys) != 1 {
		t.Fatalf("test did not isolate unknown target state from held leases: %+v", snapshot)
	}

	failTestRouteSource(route, errors.New("source failed while target toggle state was unknown"))
	select {
	case update := <-queue:
		update()
	case <-time.After(time.Second):
		t.Fatal("source failure status was not posted")
	}
	if s.liveAudio.connection != connection || s.liveAudio.retiring || s.liveAudio.recoveryAttempted || factoryCalls.Load() != 1 {
		t.Fatalf("audio recovery ran while target state was unknown: same=%t retiring=%t attempted=%t factories=%d",
			s.liveAudio.connection == connection, s.liveAudio.retiring, s.liveAudio.recoveryAttempted, factoryCalls.Load())
	}
}

func TestLiveAudioRecoveryWaitsForPhysicalPressRelease(t *testing.T) {
	s, queue := liveHostFixture(t, nil)
	first := newLiveAudioConnectionTestRoute()
	second := newLiveAudioConnectionTestRoute()
	var factoryCalls atomic.Int32
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) {
		if factoryCalls.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	}
	s.refreshLiveAudio()
	connection := s.liveAudio.connection
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("route did not finish preparation")
	}
	select {
	case update := <-queue:
		update()
	case <-time.After(time.Second):
		t.Fatal("initial route status was not posted")
	}

	device, err := bridge.NewDevice("live-host", 1)
	if err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, "ordinary-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	key, err := bridge.NewKey("Space")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.coordinator.core.Step(bridge.OrdinaryBegan(press, key)); err != nil {
		t.Fatal(err)
	}
	failTestRouteSource(first, errors.New("prepared source failed while a physical key is held"))
	select {
	case update := <-queue:
		update()
	case <-time.After(time.Second):
		t.Fatal("source failure status was not posted")
	}
	if s.liveAudio.connection != connection || s.liveAudio.retiring || s.liveAudio.recoveryAttempted {
		t.Fatal("source recovery interrupted a physical press")
	}
	s.handleDeviceEvent(devices.InputEvent{
		State:  devices.InputStateConnected,
		Device: device,
		Edges:  []devices.Edge{{Kind: devices.EdgeUp, Press: press}},
	})
	deadline := time.After(time.Second)
	for s.liveAudio.connection == connection {
		select {
		case update := <-queue:
			update()
		case <-deadline:
			t.Fatal("idle audio source was not recovered after physical release")
		}
	}
	recoveredConnection := s.liveAudio.connection
	if recoveredConnection == nil {
		t.Fatal("recovery cleared the failed connection without creating a replacement")
	}
	select {
	case <-recoveredConnection.Done():
	case <-time.After(time.Second):
		t.Fatal("replacement route did not finish preparation")
	}
	if factoryCalls.Load() != 2 {
		t.Fatalf("source recovery factory calls=%d, want 2", factoryCalls.Load())
	}
}

func failTestRouteSource(route *liveAudioConnectionTestRoute, err error) {
	route.mu.Lock()
	route.currentErr = err
	route.sourceFault = err
	route.mu.Unlock()
	select {
	case route.changes <- struct{}{}:
	default:
	}
}
