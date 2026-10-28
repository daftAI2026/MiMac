package main

import (
	"context"
	"errors"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
	"mimac.local/mimac/internal/pluginrt"
	"sync/atomic"
	"testing"
	"time"
)

func liveHostFixture(t *testing.T, outputStartErr error) (*shell, chan func()) {
	t.Helper()
	snapshot := toolShortcutsTestSnapshot("Ctrl+K")
	snapshot.AppPath = "/Applications/TestDoubao.app"
	s, _ := newToolShortcutsTestShell(t, snapshot)
	s.tools[0].id = "voice-doubao"
	s.currentVoiceTool = "voice-doubao"
	s.deviceProfiles[0].VoiceButtonID = toolShortcutsTestButton
	device, _ := bridge.NewDevice("live-host", 1)
	if _, err := s.coordinator.accept(devices.InputEvent{State: devices.InputStateConnected, Device: device}); err != nil {
		t.Fatal(err)
	}
	s.remoteState.reservation = &remoteTestReservation{}
	s.remoteState.reservationGeneration = 1
	s.liveAudio.backend = compatibilityAudioBackend
	queue := make(chan func(), 64)
	post := func(fn func()) { queue <- fn }
	s.remoteState.postMain = post
	s.audioPost = post
	noop := func() (pluginrt.StopFunc, error) { return func() error { return nil }, nil }
	runtime, err := pluginrt.New(pluginrt.Manifest{ID: corePlugin, Start: noop}, pluginrt.Manifest{ID: remotePlugin, Start: noop}, pluginrt.Manifest{ID: outputPlugin, Start: noop}, pluginrt.Manifest{ID: audioOutputPlugin, Start: func() (pluginrt.StopFunc, error) { return func() error { return nil }, outputStartErr }}, s.audioSessionManifest())
	if err != nil {
		t.Fatal(err)
	}
	s.plugins = runtime
	for _, id := range []string{corePlugin, remotePlugin, outputPlugin} {
		if err = runtime.Enable(id); err != nil {
			t.Fatal(err)
		}
	}
	s.syncVoiceBinding()
	t.Cleanup(func() {
		s.closing = true
		if s.audioSession != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.audioSession.close(ctx); err != nil {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	return s, queue
}

func pumpLiveHost(t *testing.T, queue <-chan func(), until func() bool) {
	t.Helper()
	deadline := time.After(time.Second)
	for !until() {
		select {
		case fn := <-queue:
			fn()
		case <-deadline:
			t.Fatal("host lifecycle did not complete")
		}
	}
}

func TestLiveAudioHostOwnsColdPreparationBeforeFactoryRuns(t *testing.T) {
	s, _ := liveHostFixture(t, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	route := newLiveAudioConnectionTestRoute()
	s.liveAudio.factory = func(ctx context.Context, g uint64, path string) (liveAudioRoute, error) {
		close(entered)
		select {
		case <-release:
			return route, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.refreshLiveAudio()
	<-entered
	if s.audioSession == nil || s.audioSession.closeResources == nil || s.audioResourcesReady() {
		t.Fatal("cold factory has no session cleanup owner, or became ready early")
	}
	close(release)
	<-s.liveAudio.connection.Done()
	if !s.audioResourcesReady() {
		t.Fatal("completed preparation not ready")
	}
	s.refreshLiveAudio()
	if start, _, _ := route.calls(); start != 1 {
		t.Fatalf("refresh restarted route %d times", start)
	}
}

func TestLiveAudioHostFailedPluginStartNeverOpensNativeResources(t *testing.T) {
	s, _ := liveHostFixture(t, errors.New("adapter unavailable"))
	var calls atomic.Int32
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) {
		calls.Add(1)
		return newLiveAudioConnectionTestRoute(), nil
	}
	s.refreshLiveAudio()
	if s.audioSession != nil || s.liveAudio.connection != nil || s.report.AudioError == "" {
		t.Fatal("failed registration retained an unowned warm connection")
	}
	if s.coordinator.voiceAudioBinding == nil {
		t.Fatal("failed backend allowed shortcut-only fallback")
	}
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("native factory ran before its cleanup owner was installed")
	}
}

func TestLiveAudioHostToolChangeWaitsForOldCleanup(t *testing.T) {
	s, q := liveHostFixture(t, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	old := newLiveAudioConnectionTestRoute()
	old.closeEnter = entered
	old.closeBlock = release
	next := newLiveAudioConnectionTestRoute()
	var calls atomic.Int32
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) {
		if calls.Add(1) == 1 {
			return old, nil
		}
		return next, nil
	}
	s.refreshLiveAudio()
	prior := s.liveAudio.connection
	<-prior.Done()
	s.tools[0].snapshot.AppPath = "/Applications/RelocatedDoubao.app"
	s.refreshLiveAudio()
	<-entered
	if !s.liveAudio.retiring || s.audioResourcesReady() || calls.Load() != 1 || s.liveAudio.connection != prior {
		t.Fatal("new target became available before old cleanup")
	}
	close(release)
	pumpLiveHost(t, q, func() bool { return s.liveAudio.connection != nil && s.liveAudio.connection != prior })
	<-s.liveAudio.connection.Done()
	if calls.Load() != 2 || !s.audioResourcesReady() {
		t.Fatal("replacement route did not become ready after cleanup")
	}
}

func TestLiveAudioHostStopDuringRetirementClearsRetiring(t *testing.T) {
	s, q := liveHostFixture(t, nil)
	route := newLiveAudioConnectionTestRoute()
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) {
		return route, nil
	}
	s.refreshLiveAudio()
	prior := s.liveAudio.connection
	if prior == nil {
		t.Fatal("warm connection was not created")
	}
	<-prior.Done()
	select {
	case update := <-q:
		update() // Drain the watcher's initial readiness notification.
	case <-time.After(time.Second):
		t.Fatal("initial route readiness was not posted")
	}

	s.tools[0].snapshot.AppPath = "/Applications/RelocatedDoubao.app"
	s.refreshLiveAudio()
	if !s.liveAudio.retiring {
		t.Fatal("tool change did not begin retirement")
	}
	select {
	case retireDone := <-q:
		// Queue the independent remote stop before running either completion.
		s.beginRemoteStop()
		var stopDone func()
		select {
		case stopDone = <-q:
		case <-time.After(time.Second):
			t.Fatal("remote stop completion was not posted")
		}
		stopDone()
		if s.audioSession != nil {
			t.Fatal("remote stop did not detach the audio host")
		}
		retireDone()
	case <-time.After(time.Second):
		t.Fatal("tool retirement completion was not posted")
	}
	if s.liveAudio.retiring {
		t.Fatal("detaching the retired host left live audio permanently retiring")
	}
}

func TestLiveAudioHostFailedCleanupKeepsOriginalOwner(t *testing.T) {
	s, q := liveHostFixture(t, nil)
	route := newLiveAudioConnectionTestRoute()
	route.closeErrors = []error{errors.New("restore failed"), nil}
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) { return route, nil }
	s.refreshLiveAudio()
	prior := s.liveAudio.connection
	<-prior.Done()
	s.currentVoiceTool = "voice-typeless"
	s.refreshLiveAudio()
	pumpLiveHost(t, q, func() bool { return !s.liveAudio.retiring })
	if s.liveAudio.connection != prior || s.audioSession == nil || s.audioResourcesReady() || s.report.AudioError == "" {
		t.Fatal("failed cleanup lost its owner or permitted a new press")
	}
	if !s.report.AudioCleanupFailed {
		t.Fatal("failed cleanup did not report the retained owner")
	}
	s.refreshLiveAudio()
	pumpLiveHost(t, q, func() bool { return s.audioSession == nil })
	if s.liveAudio.connection != nil || s.coordinator.voiceAudioBinding == nil {
		t.Fatal("retry did not clean connection and preserve fail-closed voice routing")
	}
	if s.report.AudioCleanupFailed {
		t.Fatal("confirmed cleanup retained a stale failure report")
	}
}

func TestLiveAudioHostUnsupportedTargetDoesNotOpenOrFallback(t *testing.T) {
	s, _ := liveHostFixture(t, nil)
	s.currentVoiceTool = "voice-typeless"
	s.tools[0].id = "voice-typeless"
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) {
		t.Error("unsupported target opened native resources")
		return nil, nil
	}
	s.refreshLiveAudio()
	if s.liveAudio.connection != nil || s.coordinator.voiceAudioBinding == nil {
		t.Fatal("unsupported target has a route or shortcut-only fallback")
	}
	if _, _, applies := s.liveAudioStatus(s.coordinator.core.Snapshot()); !applies {
		t.Fatal("missing unsupported-target status")
	}
}
