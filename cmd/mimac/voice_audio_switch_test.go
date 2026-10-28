package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
)

func TestSelectVoiceToolRetiresAppleSessionBeforeNewAudioRoute(t *testing.T) {
	s, mainTasks := liveHostFixture(t, nil)
	appleTool := s.tools[0]
	appleTool.id, appleTool.name, appleTool.kind = appleSpeechToolID, "Apple speech", voiceRecognition
	s.currentVoiceTool = appleSpeechToolID // Fixture setup; the switch below uses selectVoiceTool.

	newSnapshot := toolShortcutsTestSnapshot("Ctrl+L")
	newSnapshot.AppPath = "/Applications/TestDoubao.app"
	newTool := &voiceTool{
		id: "voice-doubao", name: "Doubao", kind: voiceInputMethod,
		active: true, sequence: 9, snapshot: newSnapshot,
	}
	s.tools = append(s.tools, newTool)
	s.toolPreferencesPath = filepath.Join(t.TempDir(), "tools.json")
	s.toolPreferences = toolPreferences{
		Version: 1, Enabled: []string{appleSpeechToolID, newTool.id}, Current: appleSpeechToolID,
	}
	s.syncVoiceBinding()
	if err := s.coordinator.attachOutput(&toolShortcutsTestSink{}); err != nil {
		t.Fatal(err)
	}

	audioTasks := make(chan func(), 16)
	s.audioPost = func(fn func()) { audioTasks <- fn }
	generation := s.coordinator.core.Snapshot().Generation
	closeErr := errors.New("device close failed once")
	var oldCloseCalls atomic.Int32
	var failFirstClose atomic.Bool
	secondCloseEntered := make(chan struct{})
	releaseSecondClose := make(chan struct{})
	var releaseSecondOnce sync.Once
	releaseSecond := func() { releaseSecondOnce.Do(func() { close(releaseSecondClose) }) }
	t.Cleanup(releaseSecond)
	oldSource := newHostPCMSource()
	oldTarget := &transcriptTargetTestStub{}
	oldRecognizer := &appleRecognizerTestStub{
		hostPCMSink: newHostPCMSink(), text: "old session transcript",
	}
	oldRoute := newVoiceAudioSwitchAppleRoute(t, generation, oldSource, oldTarget, oldRecognizer, func() error {
		attempt := oldCloseCalls.Add(1)
		if attempt == 1 && failFirstClose.Load() {
			return closeErr
		}
		if attempt == 2 {
			close(secondCloseEntered)
			<-releaseSecondClose
		}
		return nil
	})

	newSource := newHostPCMSource()
	newSink := newHostPCMSink()
	newRoute := &voiceAudioSwitchPCMRoute{
		liveAudioConnectionTestRoute: newLiveAudioConnectionTestRoute(),
		source:                       newSource, sink: newSink,
	}
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
	// The host watcher owns Changes; readiness must be observed through its
	// main-thread notifications rather than stealing that channel's signal.
	pumpLiveHost(t, mainTasks, s.audioResourcesReady)
	if !s.audioResourcesReady() {
		t.Fatal("Apple route was not ready for the old session")
	}
	oldHost := s.audioSession
	if oldHost == nil {
		t.Fatal("Apple route has no owning audio session host")
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
	runHostTask(t, audioTasks) // SourcePrepared opens Core’s capture gate.
	waitHostSignal(t, oldSource.reading)
	if snapshot := s.coordinator.core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != bridge.PhaseCapturing {
		t.Fatalf("old Apple session did not reach capture: %+v", snapshot)
	}

	deliver(oldPress, devices.EdgeUp)
	if snapshot := s.coordinator.core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != bridge.PhaseDraining {
		t.Fatalf("old Apple session did not enter drain: %+v", snapshot)
	}
	lateTranscript := runAndReturnHostTask(t, audioTasks) // Transcript exists, but its main-thread callback is held.
	if snapshot := s.coordinator.core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != bridge.PhaseDraining {
		t.Fatalf("queued transcript was not held against a draining session: %+v", snapshot)
	}

	failFirstClose.Store(true)
	if err := s.selectVoiceTool(newTool); err != nil {
		t.Fatal(err)
	}
	if s.currentVoiceTool != newTool.id {
		t.Fatalf("Current = %q, want %q", s.currentVoiceTool, newTool.id)
	}
	lateTranscript() // The host was cancelled by retirement; this old token must be dropped.
	if got := oldTarget.inserted(); len(got) != 0 {
		t.Fatalf("old queued transcript reached its target after switching: %v", got)
	}

	pumpLiveHost(t, mainTasks, func() bool {
		return oldCloseCalls.Load() == 1 && !s.liveAudio.retiring
	})
	if s.audioSession != oldHost || s.liveAudio.connection != oldConnection || channelIsClosed(oldConnection.Closed()) {
		t.Fatal("failed old close lost the original session or connection owner")
	}
	if !s.report.AudioCleanupFailed || factoryCalls.Load() != 1 || oldRecognizer.closeCalls.Load() != 1 {
		t.Fatalf("failed retirement state: cleanupFailed=%t factories=%d recognizerClose=%d", s.report.AudioCleanupFailed, factoryCalls.Load(), oldRecognizer.closeCalls.Load())
	}

	s.refreshLiveAudio() // Explicitly retry retirement after the injected first close failure.
	waitHostSignal(t, secondCloseEntered)
	if s.audioSession != oldHost || s.liveAudio.connection != oldConnection || factoryCalls.Load() != 1 || channelIsClosed(oldConnection.Closed()) {
		t.Fatal("replacement route started before the old connection close completed")
	}
	releaseSecond()
	pumpLiveHost(t, mainTasks, func() bool {
		return oldCloseCalls.Load() == 2 && s.audioSession != nil && s.audioSession != oldHost &&
			s.liveAudio.connection != nil && s.liveAudio.connection != oldConnection
	})
	newConnection := s.liveAudio.connection
	waitLiveAudioClosed(t, oldConnection)
	waitLiveAudioDone(t, newConnection)
	if oldCloseCalls.Load() != 2 || factoryCalls.Load() != 2 || !s.audioResourcesReady() {
		t.Fatalf("new route did not follow confirmed old cleanup: closes=%d factories=%d ready=%t", oldCloseCalls.Load(), factoryCalls.Load(), s.audioResourcesReady())
	}
	if s.report.AudioCleanupFailed {
		t.Fatal("confirmed retirement left the cleanup failure flag set")
	}

	newPress, err := bridge.NewPressToken(device, toolShortcutsTestButton, 2)
	if err != nil {
		t.Fatal(err)
	}
	deliver(newPress, devices.EdgeDown)
	runHostTask(t, audioTasks)
	waitHostSignal(t, newSource.reading)
	if snapshot := s.coordinator.core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != bridge.PhaseCapturing {
		t.Fatalf("new tool session did not capture after old cleanup: %+v", snapshot)
	}
	newToken := s.coordinator.core.Snapshot().Session.Token
	lateTranscript() // The retired host also stays fenced after a replacement starts.
	if snapshot := s.coordinator.core.Snapshot(); !snapshot.HasSession || snapshot.Session.Token != newToken {
		t.Fatal("old completion affected the replacement session")
	}
	deliver(newPress, devices.EdgeUp)
	if snapshot := s.coordinator.core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != bridge.PhaseDraining {
		t.Fatalf("new tool session did not drain: %+v", snapshot)
	}
	completion := runAndReturnHostTask(t, audioTasks)
	completion()
	waitHostSignal(t, newSink.closed)
	if got := <-newSink.writes; !reflect.DeepEqual(got, []int16{7, -7, 9}) {
		t.Fatalf("new tool PCM tail = %v", got)
	}
	if newSink.drains.Load() != 1 || newSource.closed.Load() != 1 || s.coordinator.core.Snapshot().HasSession {
		t.Fatal("new tool did not drain and close its own audio session")
	}
	if got := oldTarget.inserted(); len(got) != 0 {
		t.Fatalf("old session target received text during new session: %v", got)
	}
}

func newVoiceAudioSwitchAppleRoute(
	t *testing.T,
	generation uint64,
	source *hostPCMSource,
	target *transcriptTargetTestStub,
	recognizer *appleRecognizerTestStub,
	closeDevice func() error,
) *appleAudioRoute {
	t.Helper()
	var streams atomic.Int32
	route, err := newAppleAudioRoute(generation,
		func(context.Context, atvv.StreamKey) (audiosession.Source, error) {
			if streams.Add(1) == 1 {
				return &appleRouteTestPCMSource{hostPCMSource: source}, nil
			}
			return &appleRouteTestPCMSource{hostPCMSource: newHostPCMSource()}, nil
		},
		closeDevice,
		func(context.Context) (localRecognizer, error) { return recognizer, nil },
		func() (transcriptTarget, error) { return target, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	return route
}

// The external-tool path receives PCM; MiMac does not insert its transcript.
type voiceAudioSwitchPCMRoute struct {
	*liveAudioConnectionTestRoute
	source *hostPCMSource
	sink   *hostPCMSink
}

func (r *voiceAudioSwitchPCMRoute) Prepare(context.Context, bridge.SessionToken) (audiosession.Resources, error) {
	return audiosession.Resources{Source: r.source, Sink: r.sink, SampleRate: 16000}, nil
}
