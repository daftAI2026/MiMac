package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/devices"
)

func TestLocalAudioVoiceDoesNotRequireKeyOutputSink(t *testing.T) {
	plan := voiceAudioTestPlan(t, bridge.ActionHold, bridge.RequestStart, bridge.RequestFinish, "RightCommand")
	d, device, _ := newVoiceAudioTestCoordinator(t, plan)
	d.sink = nil
	d.resolveLocalVoice = func() (bridge.Binding, bool, error) {
		return bridge.NewAudioBinding(), true, nil
	}

	press := voiceAudioTestPress(t, device, 1)
	acceptVoiceAudioEdge(t, d, device, press, devices.EdgeDown)
	snapshot := d.core.Snapshot()
	if !snapshot.Configured || !snapshot.HasSession || snapshot.Session.Binding.Policy() != bridge.PolicyAudio {
		t.Fatalf("local voice did not start without the key sink: %+v", snapshot)
	}
	if len(snapshot.HeldKeys) != 0 {
		t.Fatalf("local voice acquired synthesized keys: %v", snapshot.HeldKeys)
	}
	audio := d.audio.(*routedEffectsAudioSink)
	if len(audio.effects) != 1 {
		t.Fatalf("local voice audio effects = %#v, want PrepareSource only", audio.effects)
	}
	if _, ok := audio.effects[0].(bridge.PrepareSourceEffect); !ok {
		t.Fatalf("local voice first effect = %#v, want PrepareSource", audio.effects[0])
	}

	acceptVoiceAudioEdge(t, d, device, press, devices.EdgeUp)
	if got := countEffectName(audio.effects, "StopSourceEffect"); got != 1 {
		t.Fatalf("local voice release StopSource count = %d, want 1", got)
	}
}

func TestAppleRouteRetiresWhenSelectingOtherToolWithEmptyBackend(t *testing.T) {
	s, queue := liveHostFixture(t, nil)
	tool := s.tools[0]
	tool.id = appleSpeechToolID
	s.currentVoiceTool = appleSpeechToolID
	s.liveAudio.backend = ""
	s.syncVoiceBinding()

	route := newLiveAudioConnectionTestRoute()
	s.liveAudio.factory = func(context.Context, uint64, string) (liveAudioRoute, error) {
		return route, nil
	}
	s.refreshLiveAudio()
	oldConnection := s.liveAudio.connection
	if oldConnection == nil {
		t.Fatal("selecting local Apple speech did not prepare its route")
	}
	select {
	case <-oldConnection.Done():
	case <-time.After(time.Second):
		t.Fatal("Apple route construction did not finish")
	}
	if !s.audioResourcesReady() {
		t.Fatal("Apple route did not become ready")
	}
	// Drain the asynchronous initial readiness update before changing tools.
	select {
	case update := <-queue:
		update()
	case <-time.After(time.Second):
		t.Fatal("Apple route readiness was not posted")
	}

	tool.id = "voice-other"
	s.currentVoiceTool = tool.id
	s.refreshLiveAudio()
	pumpLiveHost(t, queue, func() bool { return s.audioSession == nil && s.liveAudio.connection == nil })
	select {
	case <-oldConnection.Closed():
	default:
		t.Fatal("Apple route was detached before its connection closed")
	}
	if _, _, closeCalls := route.calls(); closeCalls != 1 {
		t.Fatalf("retired Apple route Close calls = %d, want 1", closeCalls)
	}
}

func TestAppleBeforeStartRejectsExpiredCaptureAndClosesTarget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	target := &appleRouteTestTarget{}
	route := &appleAudioRoute{
		sessions: make(map[bridge.SessionToken]*appleSessionSink),
		capture: func() (transcriptTarget, error) {
			cancel() // Deterministically model capture returning after its admission budget expired.
			return target, nil
		},
	}

	err := route.BeforeStart(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("BeforeStart() error = %v, want context.Canceled after capture expiry", err)
	}
	if target.closeCalls != 1 {
		t.Fatalf("expired capture target Close calls = %d, want 1", target.closeCalls)
	}
	if route.pending != nil {
		t.Fatal("expired capture target was retained for a later session")
	}
}

type appleRouteTestTarget struct{ closeCalls int }

func (*appleRouteTestTarget) Insert(string) error { return nil }

func (t *appleRouteTestTarget) Close() error {
	t.closeCalls++
	return nil
}
