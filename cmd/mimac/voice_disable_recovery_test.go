package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
	"mimac.local/mimac/internal/pluginrt"
	"mimac.local/mimac/internal/voicetools"
)

func TestDisableCurrentVoiceToolKeepsSelectionAndAudioOwnerConsistent(t *testing.T) {
	s, mainTasks := liveHostFixture(t, nil)
	ready := func() bool { return s.audioResourcesReady != nil && s.audioResourcesReady() }
	tool := s.tools[0]
	tool.id, tool.name, tool.kind = appleSpeechToolID, "Apple speech", voiceRecognition
	tool.snapshot.State = voicetools.Ready
	other := &voiceTool{
		id: "voice-typeless", name: "Typeless", kind: voiceInputMethod, active: true,
		sequence: 4, snapshot: toolShortcutsTestSnapshot("Ctrl+K"),
	}
	s.tools = append(s.tools, other)
	s.currentVoiceTool = tool.id
	preferencesPath := filepath.Join(t.TempDir(), "tools.json")
	s.toolPreferencesPath = preferencesPath
	s.toolPreferences = toolPreferences{Version: 1, Enabled: []string{tool.id, other.id}, Current: tool.id}
	if err := saveToolPreferences(preferencesPath, s.toolPreferences); err != nil {
		t.Fatal(err)
	}

	// This fixed test runtime models the voice-tool lifecycle without running
	// discovery or opening any external app. Audio-session still uses the real
	// manifest and host path from the shared fixture.
	noop := func() (pluginrt.StopFunc, error) { return func() error { return nil }, nil }
	manifests := []pluginrt.Manifest{
		{ID: corePlugin, Start: noop},
		{ID: remotePlugin, Start: noop},
		{ID: outputPlugin, Requires: []string{corePlugin}, Start: noop},
		{ID: audioOutputPlugin, Requires: []string{corePlugin}, Start: noop},
		s.audioSessionManifest(),
	}
	for _, candidate := range []*voiceTool{tool, other} {
		candidate := candidate
		manifests = append(manifests, pluginrt.Manifest{ID: candidate.id, Start: func() (pluginrt.StopFunc, error) {
			candidate.active = true
			return func() error {
				candidate.stop()
				if err := s.cancelToolActions(candidate.id); err != nil {
					s.outputStatus = "输出已暂停，请松开按键后恢复"
				}
				return nil
			}, nil
		}})
	}
	runtime, err := pluginrt.New(manifests...)
	if err != nil {
		t.Fatal(err)
	}
	s.plugins = runtime
	s.audioOutput = newAudioOutputOwner()
	for _, id := range []string{corePlugin, remotePlugin, outputPlugin, tool.id, other.id} {
		if err := runtime.Enable(id); err != nil {
			t.Fatal(err)
		}
	}
	s.syncVoiceBinding()

	audioTasks := make(chan func(), 8)
	s.audioPost = func(fn func()) { audioTasks <- fn }
	generation := s.coordinator.core.Snapshot().Generation
	oldSource := newHostPCMSource()
	oldTarget := &transcriptTargetTestStub{}
	oldRecognizer := &appleRecognizerTestStub{hostPCMSink: newHostPCMSink(), text: "discarded on disable"}
	closeErr := errors.New("device close failed once")
	var closeCalls atomic.Int32
	oldRoute := newVoiceAudioSwitchAppleRoute(t, generation, oldSource, oldTarget, oldRecognizer, func() error {
		if closeCalls.Add(1) == 1 {
			return closeErr
		}
		return nil
	})
	var factoryCalls atomic.Int32
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) {
		factoryCalls.Add(1)
		return oldRoute, nil
	}
	s.refreshLiveAudio()
	oldConnection := s.liveAudio.connection
	if oldConnection == nil {
		t.Fatal("Apple route was not registered with the session host")
	}
	waitLiveAudioDone(t, oldConnection)
	pumpLiveHost(t, mainTasks, s.audioResourcesReady)
	oldHost := s.audioSession
	if oldHost == nil || !ready() || !runtime.Active(tool.id) || !runtime.Active(other.id) {
		t.Fatal("selected Apple route or voice plugins were not ready")
	}

	device, err := bridge.NewDevice("live-host", generation)
	if err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, toolShortcutsTestButton, 1)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := s.coordinator.accept(devices.InputEvent{
		State: devices.InputStateConnected, Device: device,
		Edges: []devices.Edge{{Kind: devices.EdgeDown, Press: press}},
	})
	if err != nil || !accepted {
		t.Fatalf("voice down rejected: accepted=%t err=%v", accepted, err)
	}
	runHostTask(t, audioTasks)
	waitHostSignal(t, oldSource.reading)
	if snapshot := s.coordinator.core.Snapshot(); !snapshot.HasSession || snapshot.Session.Phase != bridge.PhaseCapturing {
		t.Fatalf("old Apple session did not reach capture: %+v", snapshot)
	}

	// A preference write failure must leave the selected plugin, active capture,
	// and usable route intact. An existing directory makes rename fail without
	// adding a save-error injection seam to production.
	storedBefore, err := readToolPreferences(preferencesPath)
	if err != nil {
		t.Fatal(err)
	}
	readyBeforeSaveFailure := ready()
	s.toolPreferencesPath = t.TempDir()
	if err := s.setVoiceToolEnabled(tool, false); err == nil {
		t.Fatal("disabling the selected tool succeeded despite the failed preference save")
	}
	storedAfter, err := readToolPreferences(preferencesPath)
	if err != nil || !reflect.DeepEqual(storedAfter, storedBefore) || !reflect.DeepEqual(s.toolPreferences, storedBefore) || s.currentVoiceTool != tool.id ||
		!tool.active || !runtime.Active(tool.id) || s.audioSession != oldHost || s.liveAudio.connection != oldConnection ||
		ready() != readyBeforeSaveFailure || !s.coordinator.core.Snapshot().HasSession || closeCalls.Load() != 0 {
		t.Fatalf("failed save changed active selection or capture: stored=%+v prefs=%+v current=%q active=%t runtime=%t hostSame=%t routeSame=%t ready=%t snapshot=%+v closes=%d err=%v",
			storedAfter, s.toolPreferences, s.currentVoiceTool, tool.active, runtime.Active(tool.id), s.audioSession == oldHost,
			s.liveAudio.connection == oldConnection, ready(), s.coordinator.core.Snapshot(), closeCalls.Load(), err)
	}

	s.toolPreferencesPath = preferencesPath
	if err := s.setVoiceToolEnabled(tool, false); err != nil {
		t.Fatal(err)
	}
	storedAfter, err = readToolPreferences(preferencesPath)
	if err != nil || storedAfter.Current != "" || s.toolPreferences.Current != "" || s.currentVoiceTool != "" ||
		!reflectVoiceEnabled(storedAfter.Enabled, other.id) || tool.active || runtime.Active(tool.id) || !other.active || !runtime.Active(other.id) {
		t.Fatalf("disable did not commit one empty Current and stop only its plugin: stored=%+v prefs=%+v current=%q tool=%t/%t other=%t/%t err=%v",
			storedAfter, s.toolPreferences, s.currentVoiceTool, tool.active, runtime.Active(tool.id), other.active, runtime.Active(other.id), err)
	}
	if snapshot := s.coordinator.core.Snapshot(); snapshot.HasSession {
		t.Fatalf("disabling the capturing tool left its Core session active: %+v", snapshot)
	}
	deliverVoiceDisableRelease(t, s, device, press)

	pumpLiveHost(t, mainTasks, func() bool { return s.report.AudioCleanupFailed && !s.liveAudio.retiring })
	if s.audioSession != oldHost || s.liveAudio.connection != oldConnection || channelIsClosed(oldConnection.Closed()) ||
		factoryCalls.Load() != 1 || closeCalls.Load() != 1 || ready() || !runtime.Active(audioSessionPlugin) ||
		!s.report.AudioCleanupFailed || s.currentVoiceTool != "" || tool.active || runtime.Active(tool.id) || !other.active || !runtime.Active(other.id) {
		t.Fatalf("failed audio close did not retain a fenced owner with empty Current: hostSame=%t routeSame=%t closed=%t factories=%d closes=%d ready=%t sessionPlugin=%t failed=%t current=%q tool=%t/%t other=%t/%t",
			s.audioSession == oldHost, s.liveAudio.connection == oldConnection, channelIsClosed(oldConnection.Closed()), factoryCalls.Load(),
			closeCalls.Load(), ready(), runtime.Active(audioSessionPlugin), s.report.AudioCleanupFailed,
			s.currentVoiceTool, tool.active, runtime.Active(tool.id), other.active, runtime.Active(other.id))
	}
	if snapshot := s.coordinator.core.Snapshot(); !snapshot.RouteKnown || snapshot.RouteAvailable || snapshot.HasSession {
		t.Fatalf("failed close left Core route available: %+v", snapshot)
	}
	if got := oldTarget.inserted(); len(got) != 0 {
		t.Fatalf("disabled tool delivered a transcript: %v", got)
	}

	s.refreshLiveAudio() // Explicit retry releases the retained owner; Current stays empty.
	pumpLiveHost(t, mainTasks, func() bool { return s.audioSession == nil && s.liveAudio.connection == nil })
	waitLiveAudioClosed(t, oldConnection)
	storedAfter, err = readToolPreferences(preferencesPath)
	if err != nil || storedAfter.Current != "" || s.currentVoiceTool != "" ||
		!reflectVoiceEnabled(storedAfter.Enabled, other.id) || runtime.Active(audioSessionPlugin) ||
		runtime.Active(tool.id) || !runtime.Active(other.id) || s.report.AudioCleanupFailed ||
		factoryCalls.Load() != 1 || closeCalls.Load() != 2 || s.coordinator.voiceAudioBinding == nil {
		t.Fatalf("retry did not release old owner without selecting fallback: stored=%+v current=%q sessionPlugin=%t tool=%t other=%t cleanupFailed=%t factories=%d closes=%d binding=%t err=%v",
			storedAfter, s.currentVoiceTool, runtime.Active(audioSessionPlugin), runtime.Active(tool.id), runtime.Active(other.id),
			s.report.AudioCleanupFailed, factoryCalls.Load(), closeCalls.Load(), s.coordinator.voiceAudioBinding != nil, err)
	}
}

func deliverVoiceDisableRelease(t *testing.T, s *shell, device bridge.Device, press bridge.PressToken) {
	t.Helper()
	accepted, err := s.coordinator.accept(devices.InputEvent{
		State: devices.InputStateConnected, Device: device,
		Edges: []devices.Edge{{Kind: devices.EdgeUp, Press: press}},
	})
	if err != nil || !accepted {
		t.Fatalf("voice release rejected: accepted=%t err=%v", accepted, err)
	}
}

func reflectVoiceEnabled(enabled []string, want string) bool {
	return len(enabled) == 1 && enabled[0] == want
}
