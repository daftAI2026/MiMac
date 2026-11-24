package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/pluginrt"
)

type voicePromptTestRoute struct {
	bundleID, executable string
	pid                  int32
}

func (r *voicePromptTestRoute) Start() error             { return nil }
func (r *voicePromptTestRoute) Ready() bool              { return true }
func (r *voicePromptTestRoute) Error() error             { return nil }
func (r *voicePromptTestRoute) Changes() <-chan struct{} { return nil }
func (r *voicePromptTestRoute) Prepare(context.Context, bridge.SessionToken) (audiosession.Resources, error) {
	return audiosession.Resources{}, nil
}
func (r *voicePromptTestRoute) Close(context.Context) error { return nil }
func (r *voicePromptTestRoute) voicePromptTarget() (string, int32, string, bool) {
	return r.bundleID, r.pid, r.executable, r.pid > 0
}

type voicePromptTestLease struct {
	dismissStarted chan struct{}
	restoreStarted chan struct{}
	allowDismiss   chan struct{}
	restoreGate    <-chan struct{}
	restored       chan struct{}
	dismissCalls   atomic.Int32
	restoreCalls   atomic.Int32
	dismissOnce    sync.Once
	restoreOnce    sync.Once
	restoredOnce   sync.Once
}

func newVoicePromptTestLease() *voicePromptTestLease {
	return &voicePromptTestLease{
		dismissStarted: make(chan struct{}),
		restoreStarted: make(chan struct{}),
		allowDismiss:   make(chan struct{}),
		restored:       make(chan struct{}),
	}
}

func (l *voicePromptTestLease) RestoreAfterDismissal(ctx context.Context) error {
	l.dismissCalls.Add(1)
	l.dismissOnce.Do(func() { close(l.dismissStarted) })
	select {
	case <-ctx.Done():
		return l.Restore(context.Background())
	case <-l.allowDismiss:
		l.restoredOnce.Do(func() { close(l.restored) })
		return nil
	}
}

func (l *voicePromptTestLease) Restore(context.Context) error {
	l.restoreCalls.Add(1)
	l.restoreOnce.Do(func() { close(l.restoreStarted) })
	if l.restoreGate != nil {
		<-l.restoreGate
	}
	l.restoredOnce.Do(func() { close(l.restored) })
	return nil
}

type voicePromptMaintainingTestLease struct {
	*voicePromptTestLease
	maintainStarted chan struct{}
	maintainCalls   atomic.Int32
}

func (l *voicePromptMaintainingTestLease) Maintain(ctx context.Context, sessionEnd <-chan struct{}) error {
	l.maintainCalls.Add(1)
	close(l.maintainStarted)
	select {
	case <-ctx.Done():
		return l.Restore(context.Background())
	case <-sessionEnd:
		return l.RestoreAfterDismissal(ctx)
	}
}

type voicePromptTestSuppressor struct {
	called   chan struct{}
	lease    voicePromptLease
	blockFor time.Duration
	calls    atomic.Int32
}

func (s *voicePromptTestSuppressor) Suppress(ctx context.Context) (voicePromptLease, string, error) {
	s.calls.Add(1)
	select {
	case <-ctx.Done():
		return nil, "cancelled", ctx.Err()
	default:
	}
	close(s.called)
	if s.blockFor > 0 {
		timer := time.NewTimer(s.blockFor)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, "cancelled", ctx.Err()
		case <-timer.C:
			return nil, "unchanged", nil
		}
	}
	return s.lease, "suppressed", nil
}

func TestVoicePromptPresentationKeepsNaturalRestoresSeparateAcrossQuickSessions(t *testing.T) {
	firstLease, secondLease := newVoicePromptTestLease(), newVoicePromptTestLease()
	first := &voicePromptTestSuppressor{called: make(chan struct{}), lease: firstLease}
	second := &voicePromptTestSuppressor{called: make(chan struct{}), lease: secondLease}
	var factoryCalls atomic.Int32
	s := newVoicePromptTestShell(t, func(pid int32, executable string) voicePromptSuppressor {
		if pid != 42 || executable != filepath.Join("/Applications/DoubaoIme.app", "Contents", "MacOS", "DoubaoIme") {
			t.Errorf("captured prompt target = %d %q", pid, executable)
		}
		switch factoryCalls.Add(1) {
		case 1:
			return first
		case 2:
			return second
		default:
			t.Error("suppression factory called more than once per session")
			return nil
		}
	})

	token1 := voicePromptTestToken(t, "test-source-a", 1)
	token2 := voicePromptTestToken(t, "test-source-b", 1)
	s.prepareVoicePromptPresentation()
	s.syncVoicePromptPresentation(voiceOverlayFrame{Visible: true, Token: token1, Phase: bridge.PhaseCapturing})
	waitForVoicePromptSignal(t, first.called)
	s.finishVoicePromptPresentation(token1)
	waitForVoicePromptSignal(t, firstLease.dismissStarted)

	// The old bar is still naturally fading. A new press gets an independent
	// pre-key baseline, and neither its arrival nor its lease restores window 1.
	s.prepareVoicePromptPresentation()
	s.syncVoicePromptPresentation(voiceOverlayFrame{Visible: true, Token: token2, Phase: bridge.PhaseCapturing})
	waitForVoicePromptSignal(t, second.called)
	if firstLease.restoreCalls.Load() != 0 {
		t.Fatal("new session immediately restored the still-visible previous window")
	}

	s.finishVoicePromptPresentation(token2)
	waitForVoicePromptSignal(t, secondLease.dismissStarted)
	close(firstLease.allowDismiss)
	close(secondLease.allowDismiss)
	s.waitVoicePromptPresentationRestore()
	if firstLease.dismissCalls.Load() != 1 || secondLease.dismissCalls.Load() != 1 ||
		firstLease.restoreCalls.Load() != 0 || secondLease.restoreCalls.Load() != 0 {
		t.Fatalf("natural session cleanup used the wrong path: first=%d/%d second=%d/%d",
			firstLease.dismissCalls.Load(), firstLease.restoreCalls.Load(),
			secondLease.dismissCalls.Load(), secondLease.restoreCalls.Load())
	}
}

func TestVoicePromptPresentationExplicitCloseWaitsForImmediateRestore(t *testing.T) {
	lease := newVoicePromptTestLease()
	suppressor := &voicePromptTestSuppressor{called: make(chan struct{}), lease: lease}
	s := newVoicePromptTestShell(t, func(int32, string) voicePromptSuppressor { return suppressor })
	token := voicePromptTestToken(t, "test-source", 1)
	s.prepareVoicePromptPresentation()
	s.syncVoicePromptPresentation(voiceOverlayFrame{Visible: true, Token: token, Phase: bridge.PhaseCapturing})
	waitForVoicePromptSignal(t, suppressor.called)
	s.closeVoiceOverlay()
	waitForVoicePromptSignal(t, lease.restored)
	if lease.restoreCalls.Load() != 1 || lease.dismissCalls.Load() != 0 {
		t.Fatalf("explicit close did not perform one immediate restore: immediate=%d dismissal=%d", lease.restoreCalls.Load(), lease.dismissCalls.Load())
	}
}

func TestVoicePromptSessionWorkerMaintainsAdapterLeaseUntilSessionEnd(t *testing.T) {
	base := newVoicePromptTestLease()
	lease := &voicePromptMaintainingTestLease{
		voicePromptTestLease: base,
		maintainStarted:      make(chan struct{}),
	}
	suppressor := &voicePromptTestSuppressor{called: make(chan struct{}), lease: lease}
	s := newVoicePromptTestShell(t, func(int32, string) voicePromptSuppressor { return suppressor })
	token := voicePromptTestToken(t, "test-source-maintained", 1)
	s.prepareVoicePromptPresentation()
	s.syncVoicePromptPresentation(voiceOverlayFrame{Visible: true, Token: token, Phase: bridge.PhaseCapturing})
	waitForVoicePromptSignal(t, suppressor.called)
	waitForVoicePromptSignal(t, lease.maintainStarted)
	if lease.dismissCalls.Load() != 0 || lease.restoreCalls.Load() != 0 {
		t.Fatal("maintainer lease was cleaned before the session ended")
	}
	s.finishVoicePromptPresentation(token)
	waitForVoicePromptSignal(t, lease.dismissStarted)
	close(lease.allowDismiss)
	s.waitVoicePromptPresentationRestore()
	if lease.maintainCalls.Load() != 1 || lease.dismissCalls.Load() != 1 || lease.restoreCalls.Load() != 0 {
		t.Fatalf("maintained cleanup = maintain %d, normal end %d, immediate restore %d; want 1/1/0",
			lease.maintainCalls.Load(), lease.dismissCalls.Load(), lease.restoreCalls.Load())
	}
}

func TestVoicePromptSettingOffCancelsWithoutBlockingAndQuitJoinsRestore(t *testing.T) {
	lease := newVoicePromptTestLease()
	gate := make(chan struct{})
	lease.restoreGate = gate
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	suppressor := &voicePromptTestSuppressor{called: make(chan struct{}), lease: lease}
	s := newVoicePromptTestShell(t, func(int32, string) voicePromptSuppressor { return suppressor })
	token := voicePromptTestToken(t, "test-source", 1)
	s.prepareVoicePromptPresentation()
	s.syncVoicePromptPresentation(voiceOverlayFrame{Visible: true, Token: token, Phase: bridge.PhaseCapturing})
	waitForVoicePromptSignal(t, suppressor.called)

	if err := s.applyOnlyShowMiMacVoicePrompt(false); err != nil {
		t.Fatal(err)
	}
	waitForVoicePromptSignal(t, lease.restoreStarted)
	select {
	case <-s.voiceOverlay.voicePrompt.jobs[token].done:
		t.Fatal("setting-off callback waited through the external restore")
	default:
	}

	quitDone := make(chan struct{})
	go func() {
		s.closeVoiceOverlay()
		close(quitDone)
	}()
	select {
	case <-quitDone:
		t.Fatal("host close returned while the native lease was still restoring")
	default:
	}
	close(gate)
	waitForVoicePromptSignal(t, quitDone)
	waitForVoicePromptSignal(t, lease.restored)
}

func TestVoicePromptSuppressionIsRegisteredPerTool(t *testing.T) {
	tools := builtInVoiceTools()
	var doubaoTool, wetypeTool *voiceTool
	for _, tool := range tools {
		switch tool.id {
		case "voice-doubao":
			doubaoTool = tool
		case "voice-wetype":
			wetypeTool = tool
		}
	}
	if doubaoTool == nil || doubaoTool.voicePromptSuppressor == nil || doubaoTool.voicePromptBundleID != compatDoubaoBundleID {
		t.Fatal("verified Doubao adapter has no registered prompt suppressor")
	}
	if wetypeTool == nil || wetypeTool.voicePromptSuppressor == nil || wetypeTool.voicePromptBundleID != compatWeTypeBundleID || wetypeTool.voicePromptBundleID == doubaoTool.voicePromptBundleID {
		t.Fatal("WeType needs its own registered recording-window identity")
	}
}

func newVoicePromptTestShell(t *testing.T, factory func(int32, string) voicePromptSuppressor) *shell {
	t.Helper()
	tool := &voiceTool{
		id: "voice-doubao", voicePromptBundleID: "test.voice.inputmethod",
		voicePromptSuppressor: factory,
	}
	s := &shell{
		preferencesState: preferencesState{value: hostPreferences{
			Version: preferencesVersion, VoicePresentation: voicePresentationDotBar,
			OnlyShowMiMacVoicePrompt: true, Appearance: "system",
		}, path: filepath.Join(t.TempDir(), "preferences.json")},
		voiceToolsState: voiceToolsState{tools: []*voiceTool{tool}, currentVoiceTool: tool.id},
		voiceOverlay:    voiceOverlayState{mode: voicePresentationDotBar},
		liveAudio:       liveAudioState{toolID: tool.id, appPath: "/Applications/DoubaoIme.app"},
	}
	runtime, err := pluginrt.New(s.voicePresentationManifest())
	if err != nil {
		t.Fatal(err)
	}
	s.plugins = runtime
	if err := runtime.Enable(voicePresentationPlugin); err != nil {
		t.Fatal(err)
	}
	connection := newLiveAudioConnection(func(context.Context) (liveAudioRoute, error) {
		return &voicePromptTestRoute{bundleID: tool.voicePromptBundleID, pid: 42, executable: "DoubaoIme"}, nil
	})
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("test audio route did not publish its identity")
	}
	s.liveAudio.connection = connection
	t.Cleanup(func() {
		s.stopVoicePromptPresentation()
		s.waitVoicePromptPresentationRestore()
		_ = connection.Close(context.Background())
		if err := runtime.Close(); err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})
	return s
}

func voicePromptTestToken(t *testing.T, deviceID string, generation uint64) bridge.SessionToken {
	t.Helper()
	device, err := bridge.NewDevice(deviceID, generation)
	if err != nil {
		t.Fatal(err)
	}
	core := bridge.NewUnconfiguredCore()
	for _, event := range []bridge.Event{
		bridge.DeviceConnected(device),
		bridge.Configure(bridge.NewAudioBinding()),
		bridge.RouteAvailable(true),
	} {
		if _, err := core.Step(event); err != nil {
			t.Fatal(err)
		}
	}
	press, err := bridge.NewPressToken(device, "voice", 1)
	if err != nil {
		t.Fatal(err)
	}
	effects, err := core.Step(bridge.VoiceBegan(press))
	if err != nil {
		t.Fatal(err)
	}
	for _, effect := range effects {
		if prepared, ok := effect.(bridge.PrepareSourceEffect); ok {
			return prepared.Token
		}
	}
	t.Fatalf("voice session produced no token: %#v", effects)
	return bridge.SessionToken{}
}

func waitForVoicePromptSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("voice prompt lifecycle did not reach the expected state")
	}
}
