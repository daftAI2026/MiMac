package main

import (
	"context"
	"errors"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/platform/dotscope"
	"mimac.local/mimac/internal/platform/siriglass"
	"mimac.local/mimac/internal/testutil/dotfixture"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testDotView(t *testing.T) *voiceDotView {
	t.Helper()
	model, err := dotscope.Open(dotfixture.Library(t, "../.."))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(model.Close)
	return &voiceDotView{model: model, visible: true}
}

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
	s.applyVoicePresentationSelection()
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
	s.applyVoicePresentationSelection()
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

func TestVoiceOverlayRenderFailureDoesNotStartTargetSuppression(t *testing.T) {
	for _, mode := range []voicePresentationMode{voicePresentationMiMacSphere, voicePresentationDotBar} {
		t.Run(string(mode), func(t *testing.T) {
			s, tasks, _, _ := newSessionHostTest(t)
			suppressor := &voicePromptTestSuppressor{called: make(chan struct{}), blockFor: time.Second}
			setupVoicePromptSessionTarget(t, s, mode, suppressor)
			view := &fakeVoiceOverlay{updateErr: errors.New("local surface failed")}
			s.voiceOverlay.open = func(func() voiceOverlayFrame, func(error)) (voiceOverlayView, error) {
				return view, nil
			}

			press := beginHostVoice(t, s)
			runHostTask(t, tasks)
			if phase := s.coordinator.core.Snapshot().Session.Phase; phase != bridge.PhaseCapturing {
				t.Fatalf("test did not reach the first capturing render: %v", phase)
			}
			s.syncVoiceOverlay()
			if s.voiceOverlay.err == nil || view.closes != 1 {
				t.Fatal("failed local surface was not retired")
			}
			if len(s.voiceOverlay.voicePrompt.jobs) != 0 || s.voiceOverlay.voicePrompt.baseline != nil {
				t.Fatal("target suppression started before the first local render succeeded")
			}
			select {
			case <-suppressor.called:
				t.Fatal("target suppressor was called for a failed local surface")
			default:
			}
			if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
				t.Fatal(err)
			}
			runHostTask(t, tasks)
		})
	}
}

func TestVoiceOverlayRenderDoesNotWaitForTargetWindowDiscovery(t *testing.T) {
	for _, mode := range []voicePresentationMode{voicePresentationMiMacSphere, voicePresentationDotBar} {
		t.Run(string(mode), func(t *testing.T) {
			s, tasks, _, _ := newSessionHostTest(t)
			suppressor := &voicePromptTestSuppressor{called: make(chan struct{}), blockFor: 800 * time.Millisecond}
			setupVoicePromptSessionTarget(t, s, mode, suppressor)
			view := &fakeVoiceOverlay{}
			s.voiceOverlay.open = func(func() voiceOverlayFrame, func(error)) (voiceOverlayView, error) {
				return view, nil
			}

			press := beginHostVoice(t, s)
			s.syncVoiceOverlay() // The initial preparing frame makes the local surface visible.
			if len(view.frames) != 1 || view.frames[0].Phase != bridge.PhasePreparing {
				t.Fatalf("local surface was not shown during preparation: %+v", view.frames)
			}
			runHostTask(t, tasks)
			s.syncVoiceOverlay()
			waitForVoicePromptSignal(t, suppressor.called)
			started := time.Now()
			s.syncVoiceOverlay()
			if elapsed := time.Since(started); elapsed >= suppressor.blockFor/2 {
				t.Fatalf("local render waited for target-window discovery: %s", elapsed)
			}
			if len(view.frames) < 2 || view.frames[len(view.frames)-1].Phase != bridge.PhaseCapturing {
				t.Fatalf("local surface did not update while target discovery was active: %+v", view.frames)
			}

			s.closeVoiceOverlay() // Cancels and joins the deliberately blocked lookup.
			if !s.coordinator.core.Snapshot().HasSession {
				t.Fatal("closing the local display ended the audio session")
			}
			if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
				t.Fatal(err)
			}
			runHostTask(t, tasks)
		})
	}
}

func TestFollowToolKeepsNativePromptAndDoesNotCaptureBaseline(t *testing.T) {
	s, tasks, _, _ := newSessionHostTest(t)
	suppressor := &voicePromptTestSuppressor{called: make(chan struct{})}
	setupVoicePromptSessionTarget(t, s, voicePresentationFollowTool, suppressor)
	if s.voicePresentationActive() || s.voiceOverlay.voicePrompt.baseline != nil {
		t.Fatal("follow-tool mode enabled a local surface or captured a suppression baseline")
	}
	press := beginHostVoice(t, s)
	runHostTask(t, tasks)
	s.syncVoiceOverlay()
	select {
	case <-suppressor.called:
		t.Fatal("follow-tool mode suppressed the native input-method prompt")
	default:
	}
	if !s.coordinator.core.Snapshot().HasSession {
		t.Fatal("follow-tool display mode changed the voice session")
	}
	if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	runHostTask(t, tasks)
}

func TestLocalPresentationStyleSwapKeepsTheSessionPromptLease(t *testing.T) {
	for _, transition := range [][2]voicePresentationMode{
		{voicePresentationMiMacSphere, voicePresentationDotBar},
		{voicePresentationDotBar, voicePresentationMiMacSphere},
	} {
		name := string(transition[0]) + "-to-" + string(transition[1])
		t.Run(name, func(t *testing.T) {
			s, tasks, _, _ := newSessionHostTest(t)
			lease := newVoicePromptTestLease()
			suppressor := &voicePromptTestSuppressor{called: make(chan struct{}), lease: lease}
			setupVoicePromptSessionTarget(t, s, transition[0], suppressor)
			var views []*fakeVoiceOverlay
			s.voiceOverlay.open = func(func() voiceOverlayFrame, func(error)) (voiceOverlayView, error) {
				view := &fakeVoiceOverlay{}
				views = append(views, view)
				return view, nil
			}

			press := beginHostVoice(t, s)
			s.syncVoiceOverlay()
			runHostTask(t, tasks)
			s.syncVoiceOverlay()
			waitForVoicePromptSignal(t, suppressor.called)
			token := s.coordinator.core.Snapshot().Session.Token
			job := s.voiceOverlay.voicePrompt.jobs[token]
			if job == nil {
				t.Fatal("capturing session did not own its target lease")
			}

			if err := s.applyVoicePresentationPreference(transition[1]); err != nil {
				t.Fatal(err)
			}
			if !s.voicePresentationActive() || s.voiceOverlay.mode != transition[1] || len(views) != 2 || views[0].closes != 1 {
				t.Fatal("local style replacement did not install the next display")
			}
			if s.voiceOverlay.voicePrompt.jobs[token] != job || job.explicitStop || suppressor.calls.Load() != 1 || lease.restoreCalls.Load() != 0 {
				t.Fatal("local style replacement retired or duplicated the same session prompt lease")
			}
			if !s.coordinator.core.Snapshot().HasSession || s.coordinator.core.Snapshot().Session.Token != token {
				t.Fatal("local style replacement changed the audio session")
			}

			s.finishVoicePromptPresentation(token)
			waitForVoicePromptSignal(t, lease.dismissStarted)
			close(lease.allowDismiss)
			s.waitVoicePromptPresentationRestore()
			if lease.dismissCalls.Load() != 1 || lease.restoreCalls.Load() != 0 {
				t.Fatal("natural end did not preserve the lease's dismissal path")
			}
			s.closeVoiceOverlay()
			if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
				t.Fatal(err)
			}
			runHostTask(t, tasks)
		})
	}
}

func TestFailedLocalStyleReplacementRestoresTargetPrompt(t *testing.T) {
	s, tasks, _, _ := newSessionHostTest(t)
	lease := newVoicePromptTestLease()
	suppressor := &voicePromptTestSuppressor{called: make(chan struct{}), lease: lease}
	setupVoicePromptSessionTarget(t, s, voicePresentationMiMacSphere, suppressor)
	var views []*fakeVoiceOverlay
	s.voiceOverlay.open = func(func() voiceOverlayFrame, func(error)) (voiceOverlayView, error) {
		view := &fakeVoiceOverlay{}
		if len(views) == 1 {
			view.updateErr = errors.New("replacement local surface failed")
		}
		views = append(views, view)
		return view, nil
	}

	press := beginHostVoice(t, s)
	s.syncVoiceOverlay()
	runHostTask(t, tasks)
	s.syncVoiceOverlay()
	waitForVoicePromptSignal(t, suppressor.called)
	token := s.coordinator.core.Snapshot().Session.Token
	if err := s.applyVoicePresentationPreference(voicePresentationDotBar); err != nil {
		t.Fatal(err)
	}
	waitForVoicePromptSignal(t, lease.restoreStarted)
	if s.voiceOverlay.err == nil || s.voiceOverlay.view != nil || !s.voiceOverlay.voicePrompt.jobs[token].explicitStop {
		t.Fatal("failed replacement did not retire the local view and restore the target prompt")
	}
	if !s.coordinator.core.Snapshot().HasSession || s.coordinator.core.Snapshot().Session.Token != token {
		t.Fatal("local renderer failure changed the audio session")
	}
	s.closeVoiceOverlay()
	if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	runHostTask(t, tasks)
}

func TestSwitchingToFollowToolImmediatelyRestoresTargetPrompt(t *testing.T) {
	s, tasks, _, _ := newSessionHostTest(t)
	lease := newVoicePromptTestLease()
	suppressor := &voicePromptTestSuppressor{called: make(chan struct{}), lease: lease}
	setupVoicePromptSessionTarget(t, s, voicePresentationDotBar, suppressor)
	s.voiceOverlay.open = func(func() voiceOverlayFrame, func(error)) (voiceOverlayView, error) {
		return &fakeVoiceOverlay{}, nil
	}

	press := beginHostVoice(t, s)
	s.syncVoiceOverlay()
	runHostTask(t, tasks)
	s.syncVoiceOverlay()
	waitForVoicePromptSignal(t, suppressor.called)
	token := s.coordinator.core.Snapshot().Session.Token
	if err := s.applyVoicePresentationPreference(voicePresentationFollowTool); err != nil {
		t.Fatal(err)
	}
	waitForVoicePromptSignal(t, lease.restoreStarted)
	if s.voicePresentationActive() || !s.voiceOverlay.voicePrompt.jobs[token].explicitStop {
		t.Fatal("follow-tool mode did not immediately retire local presentation ownership")
	}
	if suppressor.calls.Load() != 1 {
		t.Fatal("follow-tool mode started a new native-prompt suppression")
	}
	s.closeVoiceOverlay()
	if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	runHostTask(t, tasks)
}

func setupVoicePromptSessionTarget(t *testing.T, s *shell, mode voicePresentationMode, suppressor voicePromptSuppressor) {
	t.Helper()
	const toolID = "voice-doubao"
	const bundleID = "test.voice.inputmethod"
	tool := &voiceTool{
		id: toolID, voicePromptBundleID: bundleID,
		voicePromptSuppressor: func(int32, string) voicePromptSuppressor { return suppressor },
	}
	s.voiceToolsState = voiceToolsState{tools: []*voiceTool{tool}, currentVoiceTool: toolID}
	s.liveAudio.toolID = toolID
	s.liveAudio.appPath = "/Applications/DoubaoIme.app"
	connection := newLiveAudioConnection(func(context.Context) (liveAudioRoute, error) {
		return &voicePromptTestRoute{bundleID: bundleID, pid: 42, executable: "DoubaoIme"}, nil
	})
	waitForVoicePromptSignal(t, connection.Done())
	s.liveAudio.connection = connection
	t.Cleanup(func() {
		s.stopVoicePromptPresentation()
		s.waitVoicePromptPresentationRestore()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := connection.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	s.preferencesState.value = defaultHostPreferences()
	s.preferencesState.value.VoicePresentation = mode
	s.preferencesState.value.OnlyShowMiMacVoicePrompt = true
	s.preferencesState.path = filepath.Join(t.TempDir(), "preferences.json")
	s.applyVoicePresentationSelection()
	s.prepareVoicePromptPresentation()
}

func TestVoiceOverlayCancelHidesAndCloseFailureRetainsOwner(t *testing.T) {
	s, tasks, _, _ := newSessionHostTest(t)
	s.preferencesState.value = defaultHostPreferences()
	s.applyVoicePresentationSelection()
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
	s.applyVoicePresentationSelection()
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
	if err := s.applyVoicePresentationPreference(voicePresentationFollowTool); err != nil {
		t.Fatal(err)
	}
	if s.voicePresentationActive() || !s.plugins.Active(audioSessionPlugin) ||
		views[0].closes != 1 || s.voiceOverlayFrameAt(time.Now()).Visible {
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
	if err := s.applyVoicePresentationPreference(voicePresentationMiMacSphere); err != nil {
		t.Fatal(err)
	}
	if !s.voicePresentationActive() || opens != 2 || len(views[1].frames) != 1 || views[1].frames[0].Token != token {
		t.Fatal("re-enable did not render the same live session")
	}
	if err := s.applyVoicePresentationPreference(voicePresentationDotBar); err != nil {
		t.Fatal(err)
	}
	if opens != 3 || views[1].closes != 1 || views[2].frames[0].Token != token || s.voiceOverlay.meter.enabled {
		t.Fatal("dot selection must replace the sphere on the same token without its spectrum analysis")
	}
	source.samples <- []int16{16384, -16384}
	select {
	case output := <-sink.writes:
		if len(output) != 2 || output[0] != 16384 || output[1] != -16384 {
			t.Fatalf("switch altered PCM: %v", output)
		}
	case <-time.After(time.Second):
		t.Fatal("dot selection interrupted delivery")
	}
	now := time.Now()
	reading := s.voiceOverlayFrameAt(now)
	if reading.Phase != bridge.PhaseCapturing || reading.Token != token || reading.CenteredRMS != .5 {
		t.Fatalf("dot input did not read this audio session: %+v", reading)
	}
	if stale := s.voiceOverlayFrameAt(now.Add(time.Second)); stale.CenteredRMS != 0 {
		t.Fatalf("dot reused stale input: %v", stale.CenteredRMS)
	}
	dot := testDotView(t)
	for i := 0; i < 45; i++ {
		dot.advance(now.Add(time.Duration(i)*voiceDotPeriod), reading, false)
	}
	if dot.model.Snapshot().Pose.Level <= 0 || dot.model.Snapshot().Pose.Opacity < 0.95 {
		t.Fatal("dot field did not react to the captured PCM")
	}
	badPath := filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(badPath, 0700); err != nil {
		t.Fatal(err)
	}
	s.preferencesState.path = badPath
	if err := s.applyVoicePresentationPreference(voicePresentationFollowTool); err == nil {
		t.Fatal("write failure was ignored")
	}
	if s.preferencesState.value.VoicePresentation != voicePresentationDotBar || views[2].closes != 0 {
		t.Fatal("failed save changed visible state")
	}
	if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	runHostTask(t, tasks)
	s.closeVoiceOverlay()
}

func TestLiveTextPreferencePublishesOnlyForTheCurrentDotSession(t *testing.T) {
	s, _, _, _ := newSessionHostTest(t)
	s.preferencesState.value = defaultHostPreferences()
	s.preferencesState.value.VoicePresentation = voicePresentationDotBar
	s.preferencesState.path = filepath.Join(t.TempDir(), "preferences.json")
	if err := s.applyLiveTextPreference(true); err != nil {
		t.Fatal(err)
	}
	s.applyVoicePresentationSelection()
	press := beginHostVoice(t, s)
	first := s.coordinator.core.Snapshot().Session.Token
	if frame := s.voiceOverlayFrameAt(time.Now()); !frame.ShowLiveText || frame.Token != first {
		t.Fatalf("live text was not bound to the active dot session: %+v", frame)
	}
	if !s.voiceOverlay.interim.Publish(first, "first stable", " tentative") {
		t.Fatal("active interim publication was rejected")
	}
	if frame := s.voiceOverlayFrameAt(time.Now()); frame.InterimStable != "first stable" || frame.InterimTentative != " tentative" {
		t.Fatalf("live text frame = %+v, want current draft", frame)
	}
	if err := s.coordinator.step(bridge.RouteAvailable(false)); err != nil {
		t.Fatal(err)
	}
	if frame := s.voiceOverlayFrameAt(time.Now()); frame.InterimStable != "" || frame.InterimTentative != "" {
		t.Fatalf("cancelled session retained text: %+v", frame)
	}
	if s.voiceOverlay.interim.Publish(first, "late canceled draft", "") {
		t.Fatal("cancelled session publication was accepted")
	}
	if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	if err := s.coordinator.step(bridge.RouteAvailable(true)); err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice("synthetic-source", 1)
	if err != nil {
		t.Fatal(err)
	}
	secondPress, err := bridge.NewPressToken(device, "voice", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.coordinator.step(bridge.VoiceBegan(secondPress)); err != nil {
		t.Fatal(err)
	}
	second := s.coordinator.core.Snapshot().Session.Token
	if second == first || second.ID() == first.ID() {
		t.Fatal("Core reused the retired session ID")
	}
	frame := s.voiceOverlayFrameAt(time.Now())
	if !frame.ShowLiveText || frame.InterimStable != "" || frame.InterimTentative != "" || frame.Token != second {
		t.Fatalf("new session did not start with an empty live-text buffer: %+v", frame)
	}
	if s.voiceOverlay.interim.Publish(first, "late old draft", "") || s.voiceOverlay.interim.Snapshot(second) != (voiceInterimDraft{}) {
		t.Fatal("late old-session text crossed into the new session")
	}
	if err := s.coordinator.step(bridge.RouteAvailable(false)); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceOverlayRenderErrorHidesWithoutEndingCapture(t *testing.T) {
	s, tasks, _, _ := newSessionHostTest(t)
	s.preferencesState.value = defaultHostPreferences()
	s.applyVoicePresentationSelection()
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
	s.applyVoicePresentationSelection()
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

func TestVoiceOverlayProjectsCaptureOwnershipWithoutGuessingThinking(t *testing.T) {
	s, tasks, _, _ := newSessionHostTest(t)
	s.preferencesState.value = defaultHostPreferences()
	s.applyVoicePresentationSelection()
	check := func(visible, recording, owned bool) {
		t.Helper()
		f := s.voiceOverlayFrameAt(time.Now())
		want := siriglass.InputState{IsRecording: recording, HasVoiceInputOwnership: owned}
		if f.Visible != visible || f.Input != want {
			t.Fatalf("frame = %+v, want visible=%v input=%+v", f, visible, want)
		}
	}
	check(false, false, false)
	press := beginHostVoice(t, s)
	check(true, false, true)
	runHostTask(t, tasks)
	check(true, true, true)
	if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
		t.Fatal(err)
	}
	// Draining closes capture; it does not establish target thinking.
	check(true, false, true)
	runHostTask(t, tasks)
	check(false, false, false)
}

func TestDotViewFinishesOnlyTheSessionThatReported(t *testing.T) {
	s, tasks, _, _ := newSessionHostTest(t)
	press := beginHostVoice(t, s)
	runHostTask(t, tasks)
	token := s.coordinator.core.Snapshot().Session.Token
	now := time.Now()
	drain := voiceOverlayFrame{Visible: true, Token: token, Phase: bridge.PhaseDraining}
	view := testDotView(t)
	view.advance(now, drain, true)
	view.finish(bridge.SessionToken{}, true, now)
	if view.ended {
		t.Fatal("an unrelated completion reached the displayed session")
	}
	view.finish(token, false, now)
	view.advance(now.Add(60*time.Millisecond), voiceOverlayFrame{}, true)
	if view.model.Snapshot().Pose.Done != 0 {
		t.Fatal("quick delivery skipped the processing transition")
	}
	view.advance(now.Add(210*time.Millisecond), voiceOverlayFrame{}, true)
	if view.model.Snapshot().Pose.Done != 1 {
		t.Fatal("confirmed delivery did not render completion")
	}
	// An error before the first paint must survive the deferred motion setup.
	view = testDotView(t)
	view.selectToken(token, now)
	view.finish(token, true, now)
	view.advance(now.Add(50*time.Millisecond), voiceOverlayFrame{}, true)
	if view.model.Snapshot().Pose.Fail <= 0 {
		t.Fatal("failure before first paint was lost")
	}
	view = testDotView(t)
	view.selectToken(token, now)
	view.advance(now.Add(time.Second), drain, true)
	idle := voiceOverlayFrame{}
	for tick := 0; tick < 120; tick++ {
		if !view.advance(now.Add(time.Second+time.Duration(tick)*voiceDotPeriod), idle, true) {
			if view.model.Snapshot().Pose.Done != 0 || view.model.Snapshot().Pose.Fail > 0 {
				t.Fatal("an ordinary drain reported a transcription result")
			}
			if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
				t.Fatal(err)
			}
			runHostTask(t, tasks)
			return
		}
	}
	t.Fatal("closing the session never stopped requesting frames")
}
