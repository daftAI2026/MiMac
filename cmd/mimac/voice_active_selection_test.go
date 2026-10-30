package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
)

func TestSelectVoiceToolDuringCaptureCancelsOldSessionBeforeReplacement(t *testing.T) {
	s, mainTasks := liveHostFixture(t, nil)
	oldTool := s.tools[0]
	oldTool.id, oldTool.name, oldTool.kind = appleSpeechToolID, "Apple speech", voiceRecognition
	s.currentVoiceTool = oldTool.id

	newSnapshot := toolShortcutsTestSnapshot("Ctrl+L")
	newSnapshot.AppPath = "/Applications/TestDoubao.app"
	newTool := &voiceTool{id: "voice-doubao", name: "Doubao", kind: voiceInputMethod, active: true, sequence: 9, snapshot: newSnapshot, audioRouteFactory: testAudioRouteFactory}
	s.tools = append(s.tools, newTool)
	s.toolPreferencesPath = filepath.Join(t.TempDir(), "tools.json")
	s.toolPreferences = toolPreferences{Version: 1, Enabled: []string{oldTool.id, newTool.id}, Current: oldTool.id}
	s.syncVoiceBinding()
	if err := s.coordinator.attachOutput(&toolShortcutsTestSink{}); err != nil {
		t.Fatal(err)
	}

	audioTasks := make(chan func(), 16)
	s.audioPost = func(fn func()) { audioTasks <- fn }
	generation := s.coordinator.core.Snapshot().Generation
	oldSource := newHostPCMSource()
	oldTarget := &transcriptTargetTestStub{}
	oldRecognizer := &appleRecognizerTestStub{hostPCMSink: newHostPCMSink(), text: "late old transcript"}
	closeErr := errors.New("device close failed once")
	var closeCalls atomic.Int32
	closeEntered, releaseClose := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseClose) }) }
	t.Cleanup(release)
	oldRoute := newVoiceAudioSwitchAppleRoute(t, generation, oldSource, oldTarget, oldRecognizer, func() error {
		if closeCalls.Add(1) == 1 {
			close(closeEntered)
			<-releaseClose
			return closeErr
		}
		return nil
	})
	newSource, newSink := newHostPCMSource(), newHostPCMSink()
	newRoute := &voiceAudioSwitchPCMRoute{liveAudioConnectionTestRoute: newLiveAudioConnectionTestRoute(), source: newSource, sink: newSink}
	var factoryCalls atomic.Int32
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) {
		switch factoryCalls.Add(1) {
		case 1:
			return oldRoute, nil
		case 2:
			return newRoute, nil
		default:
			return nil, errors.New("unexpected extra route construction")
		}
	}

	s.refreshLiveAudio()
	oldConnection := s.liveAudio.connection
	if oldConnection == nil {
		t.Fatal("Apple route was not registered with the session host")
	}
	waitLiveAudioDone(t, oldConnection)
	pumpLiveHost(t, mainTasks, s.audioResourcesReady)
	oldHost := s.audioSession
	if oldHost == nil || !s.audioResourcesReady() {
		t.Fatal("Apple route was not ready for the old session")
	}

	device, err := bridge.NewDevice("live-host", generation)
	if err != nil {
		t.Fatal(err)
	}
	deliver := func(press bridge.PressToken, kind devices.EdgeKind) {
		t.Helper()
		accepted, err := s.coordinator.accept(devices.InputEvent{
			State: devices.InputStateConnected, Device: device,
			Edges: []devices.Edge{{Kind: kind, Press: press}},
		})
		if err != nil || !accepted {
			t.Fatalf("voice edge rejected: accepted=%v err=%v", accepted, err)
		}
	}
	oldPress, err := bridge.NewPressToken(device, toolShortcutsTestButton, 1)
	if err != nil {
		t.Fatal(err)
	}
	deliver(oldPress, devices.EdgeDown)
	runHostTask(t, audioTasks)
	waitHostSignal(t, oldSource.reading)
	oldSession := s.coordinator.core.Snapshot().Session
	if snapshot := s.coordinator.core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != bridge.PhaseCapturing {
		t.Fatalf("old Apple session did not reach capture: %+v", snapshot)
	}

	// This is the same shared selector used by the mappings menu and tools page.
	if err := s.selectVoiceTool(newTool); err != nil {
		t.Fatal(err)
	}
	stored, err := readToolPreferences(s.toolPreferencesPath)
	if err != nil || stored.Current != newTool.id || s.currentVoiceTool != newTool.id {
		t.Fatalf("selected target was not committed once: stored=%+v current=%q err=%v", stored, s.currentVoiceTool, err)
	}
	if snapshot := s.coordinator.core.Snapshot(); snapshot.HasSession || s.audioResourcesReady() {
		t.Fatalf("selection did not cancel/fence the capturing session: %+v ready=%t", snapshot, s.audioResourcesReady())
	}
	waitHostSignal(t, closeEntered) // Session resources are disposed; device-owner Close is held open.
	if s.audioSession != oldHost || s.liveAudio.connection != oldConnection || channelIsClosed(oldConnection.Closed()) || factoryCalls.Load() != 1 {
		t.Fatal("replacement became available before old owner cleanup completed")
	}
	if err := oldRoute.DeliverTranscript(oldSession.Token, "late old transcript"); err == nil {
		t.Fatal("retired Apple session accepted a late transcript")
	}
	if got := oldTarget.inserted(); len(got) != 0 {
		t.Fatalf("late old transcript reached its captured target: %v", got)
	}

	release()
	pumpLiveHost(t, mainTasks, func() bool { return s.report.AudioCleanupFailed && !s.liveAudio.retiring })
	if s.audioSession != oldHost || s.liveAudio.connection != oldConnection || channelIsClosed(oldConnection.Closed()) ||
		factoryCalls.Load() != 1 || s.audioResourcesReady() {
		t.Fatal("failed old close lost its owner or opened the replacement route")
	}
	if !s.report.AudioCleanupFailed {
		t.Fatal("failed old cleanup was not reported")
	}

	deliver(oldPress, devices.EdgeUp)
	s.refreshLiveAudio() // Retry the retained close after its injected one-time failure.
	pumpLiveHost(t, mainTasks, func() bool {
		return closeCalls.Load() == 2 && s.audioSession != nil && s.audioSession != oldHost &&
			s.liveAudio.connection != nil && s.liveAudio.connection != oldConnection
	})
	newConnection := s.liveAudio.connection
	waitLiveAudioClosed(t, oldConnection)
	waitLiveAudioDone(t, newConnection)
	pumpLiveHost(t, mainTasks, s.audioResourcesReady)
	if factoryCalls.Load() != 2 || !s.audioResourcesReady() || s.currentVoiceTool != newTool.id || s.report.AudioCleanupFailed {
		t.Fatalf("replacement route did not recover after old cleanup: factories=%d ready=%t current=%q cleanupFailed=%t",
			factoryCalls.Load(), s.audioResourcesReady(), s.currentVoiceTool, s.report.AudioCleanupFailed)
	}

	newPress, err := bridge.NewPressToken(device, toolShortcutsTestButton, 2)
	if err != nil {
		t.Fatal(err)
	}
	deliver(newPress, devices.EdgeDown)
	runHostTask(t, audioTasks)
	waitHostSignal(t, newSource.reading)
	if snapshot := s.coordinator.core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != bridge.PhaseCapturing {
		t.Fatalf("new target did not capture after cleanup: %+v", snapshot)
	}
	if got := oldTarget.inserted(); len(got) != 0 {
		t.Fatalf("old transcript contaminated the replacement target: %v", got)
	}
	deliver(newPress, devices.EdgeUp)
	completion := runAndReturnHostTask(t, audioTasks)
	completion()
	waitHostSignal(t, newSink.closed)
	if got := <-newSink.writes; !reflect.DeepEqual(got, []int16{7, -7, 9}) {
		t.Fatalf("new target PCM tail = %v", got)
	}
}
