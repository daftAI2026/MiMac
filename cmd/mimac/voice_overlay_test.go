package main

import (
	"errors"
	"mimac.local/mimac/internal/bridge"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeVoiceOverlay struct {
	frames              []voiceOverlayFrame
	hides, closes       int
	updateErr, closeErr error
}

func (v *fakeVoiceOverlay) Update(f voiceOverlayFrame) error {
	v.frames = append(v.frames, f)
	return v.updateErr
}
func (v *fakeVoiceOverlay) Hide() error  { v.hides++; return nil }
func (v *fakeVoiceOverlay) Close() error { v.closes++; return v.closeErr }

func TestVoiceOverlayTracksRealSessionWithoutOwningAudio(t *testing.T) {
	s, tasks, source, sink := newSessionHostTest(t)
	s.preferencesState.value = defaultHostPreferences()
	view := &fakeVoiceOverlay{}
	opened := 0
	s.voiceOverlay.open = func(func() voiceOverlayFrame, func(error)) (voiceOverlayView, error) { opened++; return view, nil }
	s.syncVoiceOverlay()
	if opened != 0 {
		t.Fatal("idle created a renderer")
	}
	press := beginHostVoice(t, s)
	s.syncVoiceOverlay()
	if opened != 1 || len(view.frames) != 1 || view.frames[0].Label != "正在准备语音" {
		t.Fatalf("preparation not presented: opens=%d frames=%+v", opened, view.frames)
	}
	runHostTask(t, tasks)
	source.samples <- []int16{-32768, 0, 0, 0}
	select {
	case <-sink.writes:
	case <-time.After(time.Second):
		t.Fatal("source did not reach sink")
	}
	f := s.voiceOverlayFrameAt(time.Now())
	if !f.Visible || f.Label != "正在收音" || f.DB < -6.1 || f.DB > -5.9 || f.Token != s.coordinator.core.Snapshot().Session.Token {
		t.Fatalf("current PCM not presented: %+v", f)
	}
	if stale := s.voiceOverlayFrameAt(time.Now().Add(time.Second)); !stale.Visible || stale.DB != -60 {
		t.Fatalf("stale level remained loud: %+v", stale)
	}
	before := s.coordinator.core.Snapshot()
	s.closeVoiceOverlay()
	if after := s.coordinator.core.Snapshot(); !after.HasSession || after.Session.Token != before.Session.Token {
		t.Fatal("closing display changed audio session")
	}
	if view.closes != 1 {
		t.Fatal("display did not release renderer")
	}
	if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	runHostTask(t, tasks)
	s.syncVoiceOverlay()
	if opened != 1 || s.voiceOverlayFrameAt(time.Now()).Visible {
		t.Fatal("drained session recreated the renderer")
	}
}

func TestVoiceOverlayFailureDoesNotFailAudioOrRetry(t *testing.T) {
	s, tasks, _, _ := newSessionHostTest(t)
	s.preferencesState.value = defaultHostPreferences()
	opened := 0
	failed := errors.New("display unavailable")
	s.voiceOverlay.open = func(func() voiceOverlayFrame, func(error)) (voiceOverlayView, error) { opened++; return nil, failed }
	press := beginHostVoice(t, s)
	s.syncVoiceOverlay()
	s.syncVoiceOverlay()
	if opened != 1 || s.voiceOverlay.err == nil {
		t.Fatal("renderer failure retried or was not recorded")
	}
	if !s.coordinator.core.Snapshot().HasSession || s.report.AudioError != "" || s.report.Error != "" {
		t.Fatal("display failure contaminated business result")
	}
	runHostTask(t, tasks)
	if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	runHostTask(t, tasks)
}

func TestVoiceOverlayCancelHidesAndCloseFailureRetainsOwner(t *testing.T) {
	s, tasks, _, _ := newSessionHostTest(t)
	s.preferencesState.value = defaultHostPreferences()
	view := &fakeVoiceOverlay{}
	s.voiceOverlay.open = func(func() voiceOverlayFrame, func(error)) (voiceOverlayView, error) { return view, nil }
	beginHostVoice(t, s)
	s.syncVoiceOverlay()
	runHostTask(t, tasks)
	if err := s.coordinator.step(bridge.RouteAvailable(false)); err != nil {
		t.Fatal(err)
	}
	s.syncVoiceOverlay()
	if view.hides == 0 || s.voiceOverlayFrameAt(time.Now()).Visible {
		t.Fatal("cancelled session stayed visible")
	}
	view.closeErr = errors.New("pending native cleanup")
	s.closeVoiceOverlay()
	if s.voiceOverlay.view != view || s.voiceOverlay.err == nil {
		t.Fatal("failed close discarded renderer owner")
	}
	view.closeErr = nil
	s.closeVoiceOverlay()
	s.closeVoiceOverlay()
	if s.voiceOverlay.view != nil || view.closes != 2 {
		t.Fatal("successful retry did not release once")
	}
}

func TestVoiceOverlayPreferenceOnlyChangesDisplay(t *testing.T) {
	s, tasks, source, sink := newSessionHostTest(t)
	s.preferencesState.value = defaultHostPreferences()
	s.preferencesState.path = filepath.Join(t.TempDir(), "preferences.json")
	opens := 0
	views := []*fakeVoiceOverlay{}
	s.voiceOverlay.open = func(func() voiceOverlayFrame, func(error)) (voiceOverlayView, error) {
		opens++
		v := &fakeVoiceOverlay{}
		views = append(views, v)
		return v, nil
	}
	press := beginHostVoice(t, s)
	s.syncVoiceOverlay()
	runHostTask(t, tasks)
	token := s.coordinator.core.Snapshot().Session.Token
	if err := s.applyVoiceOverlayPreference(false); err != nil {
		t.Fatal(err)
	}
	if views[0].closes != 1 || s.voiceOverlayFrameAt(time.Now()).Visible {
		t.Fatal("off preference did not retire the visible resources")
	}
	source.samples <- []int16{16000, 0}
	select {
	case <-sink.writes:
	case <-time.After(time.Second):
		t.Fatal("turning off display stopped PCM")
	}
	if !s.coordinator.core.Snapshot().HasSession || s.coordinator.core.Snapshot().Session.Token != token {
		t.Fatal("preference replaced the audio session")
	}
	if err := s.applyVoiceOverlayPreference(true); err != nil {
		t.Fatal(err)
	}
	if opens != 2 || len(views[1].frames) != 1 || views[1].frames[0].Token != token {
		t.Fatal("re-enable did not render the same live session")
	}
	badPath := filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(badPath, 0700); err != nil {
		t.Fatal(err)
	}
	s.preferencesState.path = badPath
	if err := s.applyVoiceOverlayPreference(false); err == nil {
		t.Fatal("write failure was ignored")
	}
	if !s.preferencesState.value.ShowVoiceOverlay || views[1].closes != 0 {
		t.Fatal("failed save changed visible state")
	}
	if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	runHostTask(t, tasks)
	s.closeVoiceOverlay()
}

func TestVoiceOverlayRenderErrorHidesWithoutEndingCapture(t *testing.T) {
	s, tasks, _, _ := newSessionHostTest(t)
	s.preferencesState.value = defaultHostPreferences()
	v := &fakeVoiceOverlay{updateErr: errors.New("native update rejected")}
	s.voiceOverlay.open = func(func() voiceOverlayFrame, func(error)) (voiceOverlayView, error) { return v, nil }
	press := beginHostVoice(t, s)
	s.syncVoiceOverlay()
	if v.hides != 1 || v.closes != 1 || s.voiceOverlay.view != nil {
		t.Fatal("failed renderer remained visible or owned")
	}
	if !s.coordinator.core.Snapshot().HasSession || s.report.AudioError != "" {
		t.Fatal("render failure cancelled real capture")
	}
	runHostTask(t, tasks)
	if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	runHostTask(t, tasks)
}

func TestVoiceOverlayRejectsKeysAndMissingAudioOwner(t *testing.T) {
	s := &shell{coordinator: newDeviceCoordinator()}
	s.preferencesState.value = defaultHostPreferences()
	device, _ := bridge.NewDevice("ordinary", 1)
	s.coordinator.sink = &routedEffectsKeySink{}
	for _, event := range []bridge.Event{bridge.DeviceConnected(device), bridge.ScopeAvailable(true), bridge.Configure(bridge.NewAudioBinding())} {
		if err := s.coordinator.step(event); err != nil {
			t.Fatal(err)
		}
	}
	press, _ := bridge.NewPressToken(device, "up", 1)
	if err := s.coordinator.step(bridge.OrdinaryBegan(press, "Space")); err != nil {
		t.Fatal(err)
	}
	if len(s.coordinator.core.Snapshot().HeldKeys) == 0 {
		t.Fatal("test did not create ordinary output")
	}
	if s.voiceOverlayFrameAt(time.Now()).Visible {
		t.Fatal("ordinary output pretended to be voice capture")
	}
	if err := s.coordinator.step(bridge.OrdinaryEnded(press)); err != nil {
		t.Fatal(err)
	}
}
