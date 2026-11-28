package main

import (
	"context"
	"errors"
	"reflect"
	"sync"
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

func TestSourcePCMReceiptOnlyRearmsMatchingTerminalLiveSession(t *testing.T) {
	device, err := bridge.NewDevice("live-host", 1)
	if err != nil {
		t.Fatal(err)
	}
	core, err := bridge.NewCore(bridge.NewAudioBinding())
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{bridge.DeviceConnected(device), bridge.RouteAvailable(true)} {
		if _, err := core.Step(event); err != nil {
			t.Fatal(err)
		}
	}
	press, err := bridge.NewPressToken(device, toolShortcutsTestButton, 41)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.Step(bridge.VoiceBegan(press)); err != nil {
		t.Fatal(err)
	}
	before := core.Snapshot()
	if !before.HasSession {
		t.Fatal("test session did not start")
	}
	if _, err := core.Step(bridge.SourceFailed(before.Session.Token)); err != nil {
		t.Fatal(err)
	}
	after := core.Snapshot()
	current := &liveAudioConnection{}
	prepared := audiosession.Completion{Event: bridge.SourcePrepared(before.Session.Token, "audio-stream"), SourcePCMRead: true}
	zeroSamples := audiosession.Completion{Event: bridge.SourceFailed(before.Session.Token)}
	staleToken := audiosession.Completion{Event: bridge.SourceFailed(bridge.SessionToken{}), SourcePCMRead: true}
	cleanupFailure := audiosession.Completion{Event: bridge.SourceFailed(before.Session.Token), CleanupFailed: true, SourcePCMRead: true}
	changed := after
	changed.Generation++
	stillActive := after
	stillActive.HasSession = true

	tests := []struct {
		name       string
		completion audiosession.Completion
		after      bridge.Snapshot
		host       *liveAudioConnection
		current    *liveAudioConnection
		generation uint64
		want       bool
	}{
		{"matching failed session after source PCM", audiosession.Completion{Event: bridge.SourceFailed(before.Session.Token), SourcePCMRead: true}, after, current, current, before.Generation, true},
		{"matching drained session after source PCM", audiosession.Completion{Event: bridge.SourceDrained(before.Session.Token), SourcePCMRead: true}, after, current, current, before.Generation, true},
		{"prepared only", prepared, after, current, current, before.Generation, false},
		{"zero samples", zeroSamples, after, current, current, before.Generation, false},
		{"empty drained session", audiosession.Completion{Event: bridge.SourceDrained(before.Session.Token)}, after, current, current, before.Generation, false},
		{"stale session token", staleToken, after, current, current, before.Generation, false},
		{"connection already changed", audiosession.Completion{Event: bridge.SourceFailed(before.Session.Token), SourcePCMRead: true}, after, current, &liveAudioConnection{}, before.Generation, false},
		{"generation changed", audiosession.Completion{Event: bridge.SourceFailed(before.Session.Token), SourcePCMRead: true}, changed, current, current, before.Generation, false},
		{"cleanup failed", cleanupFailure, after, current, current, before.Generation, false},
		{"terminal completion left a session active", audiosession.Completion{Event: bridge.SourceFailed(before.Session.Token), SourcePCMRead: true}, stillActive, current, current, before.Generation, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := sourcePCMReceiptMatchesCurrentLiveSession(test.completion, before, test.after, test.host, test.current, test.generation)
			if got != test.want {
				t.Fatalf("receipt match = %t, want %t", got, test.want)
			}
		})
	}
}

func TestLiveAudioRecoveryRearmsAfterPCMAcrossReleaseRace(t *testing.T) {
	t.Run("release before completion", func(t *testing.T) {
		runLiveAudioRecoveryAfterPCMReleaseRace(t, true)
	})
	t.Run("completion before release", func(t *testing.T) {
		runLiveAudioRecoveryAfterPCMReleaseRace(t, false)
	})
}

func runLiveAudioRecoveryAfterPCMReleaseRace(t *testing.T, releaseBeforeCompletion bool) {
	t.Helper()
	s, queue := liveHostFixture(t, nil)
	route1 := newLiveAudioConnectionTestRoute()
	sink := newStopAfterOneWriteSink()
	t.Cleanup(sink.releaseSecondWrite)
	source := newHostPCMSource()
	route2 := newLiveAudioConnectionTestRoute()
	route2.resources = audiosession.Resources{Source: source, Sink: sink, SampleRate: 16000}
	route3 := newLiveAudioConnectionTestRoute()
	routes := []*liveAudioConnectionTestRoute{route1, route2, route3}
	var factoryCalls atomic.Int32
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) {
		index := int(factoryCalls.Add(1)) - 1
		if index >= len(routes) {
			return nil, errors.New("unexpected automatic route rebuild")
		}
		return routes[index], nil
	}
	keys := &routedEffectsKeySink{}
	if err := s.coordinator.attachOutput(keys); err != nil {
		t.Fatal(err)
	}
	plan := voiceAudioTestPlan(t, bridge.ActionHold, bridge.RequestStart, bridge.RequestFinish, "RightCommand")
	s.coordinator.resolveVoiceAction = func() (bridge.ActionPlan, error) { return plan, nil }
	s.uninsertedDictation = "synthetic prior transcript"
	s.refreshLiveAudio()
	connection1 := s.liveAudio.connection
	waitLiveAudioDone(t, connection1)
	pumpLiveHost(t, queue, func() bool { return connection1.Ready() })
	firstFault := errors.New("first source failure")
	failTestRouteSource(route1, firstFault)
	pumpLiveHost(t, queue, func() bool { return s.liveAudio.connection != connection1 })
	connection2 := s.liveAudio.connection
	if connection2 == nil {
		t.Fatal("initial source fault did not create its bounded replacement")
	}
	waitLiveAudioDone(t, connection2)
	pumpLiveHost(t, queue, func() bool { return connection2.Ready() })
	if !s.liveAudio.recoveryAttempted || !s.audioResourcesReady() {
		t.Fatal("replacement route did not retain the spent recovery allowance")
	}

	device, err := bridge.NewDevice("live-host", 1)
	if err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, toolShortcutsTestButton, 51)
	if err != nil {
		t.Fatal(err)
	}
	s.handleDeviceEvent(devices.InputEvent{State: devices.InputStateConnected, Device: device, Edges: []devices.Edge{{Kind: devices.EdgeDown, Press: press}}})
	pumpLiveHost(t, queue, func() bool {
		snapshot := s.coordinator.core.Snapshot()
		return snapshot.HasSession && snapshot.Session.Phase == bridge.PhaseCapturing
	})
	waitHostSignal(t, source.reading)
	source.samples <- []int16{31, -32, 33}
	waitHostSignal(t, sink.firstAccepted)
	sourceFault := errors.New("source fault after first PCM")
	failTestRouteSource(route2, sourceFault)
	pumpLiveHost(t, queue, func() bool { return s.report.AudioError == sourceFault.Error() })
	source.samples <- []int16{41, -42, 43}
	waitHostSignal(t, sink.secondStarted)

	if releaseBeforeCompletion {
		// Release wins the race with the terminal completion. The current session
		// still owns accepted PCM and retained text while it drains.
		s.handleDeviceEvent(devices.InputEvent{State: devices.InputStateConnected, Device: device, Edges: []devices.Edge{{Kind: devices.EdgeUp, Press: press}}})
		if snapshot := s.coordinator.core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != bridge.PhaseDraining {
			t.Fatalf("release did not leave the failed write owned by the draining session: %+v", snapshot)
		}
		if !s.liveAudio.recoveryAttempted || s.liveAudio.connection != connection2 {
			t.Fatal("recovery interrupted the old session before its terminal completion")
		}
	}
	sink.releaseSecondWrite()
	if !releaseBeforeCompletion {
		pumpLiveHost(t, queue, func() bool {
			return !s.coordinator.core.Snapshot().HasSession && !s.liveAudio.recoveryAttempted
		})
		if s.liveAudio.connection != connection2 || s.liveAudio.retiring {
			t.Fatal("recovery retired the source before the physical press was released")
		}
		s.handleDeviceEvent(devices.InputEvent{State: devices.InputStateConnected, Device: device, Edges: []devices.Edge{{Kind: devices.EdgeUp, Press: press}}})
	}
	if s.uninsertedDictation != "synthetic prior transcript" {
		t.Fatal("the failed session changed the prior unsaved transcript")
	}

	pumpLiveHost(t, queue, func() bool {
		return s.liveAudio.connection != connection2 && !s.coordinator.core.Snapshot().HasSession
	})
	connection3 := s.liveAudio.connection
	if connection3 == nil {
		t.Fatal("source PCM receipt did not permit the next bounded recovery after release")
	}
	waitLiveAudioDone(t, connection3)
	pumpLiveHost(t, queue, func() bool { return connection3.Ready() })
	if factoryCalls.Load() != 3 {
		t.Fatalf("route factory calls = %d, want initial route plus two bounded rebuilds", factoryCalls.Load())
	}
	if got := sink.acceptedWrites(); !reflect.DeepEqual(got, [][]int16{{31, -32, 33}}) {
		t.Fatalf("accepted PCM changed across recovery: %v", got)
	}
	if s.uninsertedDictation != "synthetic prior transcript" {
		t.Fatal("recovery cleared the prior unsaved transcript")
	}
	if starts := countVoiceStartEffects(keys); starts != 1 {
		t.Fatalf("recovery replayed a target start action: starts=%d", starts)
	}
}

type stopAfterOneWriteSink struct {
	mu            sync.Mutex
	writes        [][]int16
	calls         int
	firstAccepted chan struct{}
	secondStarted chan struct{}
	allowSecond   chan struct{}
	allowOnce     sync.Once
}

func newStopAfterOneWriteSink() *stopAfterOneWriteSink {
	return &stopAfterOneWriteSink{
		firstAccepted: make(chan struct{}), secondStarted: make(chan struct{}), allowSecond: make(chan struct{}),
	}
}

func (s *stopAfterOneWriteSink) TryWrite(pcm []int16) error {
	s.mu.Lock()
	s.calls++
	switch s.calls {
	case 1:
		s.writes = append(s.writes, append([]int16(nil), pcm...))
		close(s.firstAccepted)
		s.mu.Unlock()
		return nil
	case 2:
		close(s.secondStarted)
		allow := s.allowSecond
		s.mu.Unlock()
		<-allow
		return errCompatReceiverStopped
	default:
		s.mu.Unlock()
		return errCompatReceiverStopped
	}
}

func (*stopAfterOneWriteSink) Drain(context.Context) error { return nil }
func (*stopAfterOneWriteSink) Close() error                { return nil }

func (s *stopAfterOneWriteSink) releaseSecondWrite() {
	s.allowOnce.Do(func() { close(s.allowSecond) })
}

func (s *stopAfterOneWriteSink) acceptedWrites() [][]int16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([][]int16, len(s.writes))
	for i := range s.writes {
		result[i] = append([]int16(nil), s.writes[i]...)
	}
	return result
}

func countVoiceStartEffects(sink *routedEffectsKeySink) int {
	count := 0
	for _, batch := range sink.batches {
		for _, effect := range batch {
			switch effect.(type) {
			case bridge.KeyDownEffect, bridge.TargetTapEffect:
				count++
			}
		}
	}
	return count
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
