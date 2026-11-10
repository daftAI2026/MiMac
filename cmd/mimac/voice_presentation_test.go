package main

import (
	"context"
	"testing"
	"time"

	"mimac.local/mimac/internal/bridge"
)

func TestVoiceInputReportUsesCurrentRuntimeWithoutCreatingAConsumer(t *testing.T) {
	for _, finish := range []string{"drain", "runtime-close"} {
		t.Run(finish, func(t *testing.T) {
			s, tasks, source, sink := newSessionHostTest(t)
			press := beginHostVoice(t, s)
			if got := s.coordinator.report(); got.AudioPhase != "preparing" || got.AudioInput != nil {
				t.Fatalf("preparation invented input: %+v", got)
			}
			runHostTask(t, tasks)
			source.samples <- []int16{-32768, 0, 0, 0}
			select {
			case <-sink.writes:
			case <-time.After(time.Second):
				t.Fatal("PCM was not forwarded")
			}
			got := s.coordinator.report()
			if got.AudioPhase != "capturing" || got.AudioInput == nil || got.AudioInput.RMS != .5 || got.AudioInput.Peak != 1 || got.AudioInput.Samples != 4 {
				t.Fatalf("missing actual source level: %+v, level=%+v", got, got.AudioInput)
			}
			// Input observations use the existing worker and read-only snapshot;
			// they must never enqueue a new main-thread completion per block.
			select {
			case <-tasks:
				t.Fatal("meter queued a UI/control completion")
			default:
			}
			if finish == "drain" {
				if err := s.coordinator.step(bridge.VoiceEnded(press)); err != nil {
					t.Fatal(err)
				}
				runHostTask(t, tasks)
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := s.audioSession.runtime.Close(ctx); err != nil {
					t.Fatal(err)
				}
				if s.coordinator.report().AudioPhase != "capturing" {
					t.Fatal("test did not preserve a stale Core phase")
				}
			}
			if s.coordinator.report().AudioInput != nil {
				t.Fatal("closed input survived the presentation boundary")
			}
		})
	}
}

func TestVoicePresentationFollowsCoreSessionAndRetiresOldResults(t *testing.T) {
	s, _ := liveHostFixture(t, nil)
	// Exercise the actual coordinator and status consumer without native I/O.
	s.liveAudio.backend = ""
	s.coordinator.sink = &routedEffectsKeySink{}
	s.coordinator.audio = &routedEffectsAudioSink{}
	step := func(event bridge.Event) {
		t.Helper()
		if err := s.coordinator.step(event); err != nil {
			t.Fatal(err)
		}
	}
	check := func(label string) {
		t.Helper()
		got, tone := s.remoteStatus()
		if got != label || tone != toneActive {
			t.Fatalf("status = %q/%v, want %q/active", got, tone, label)
		}
		report := s.coordinator.report()
		wantPhase := map[string]string{"正在准备语音": "preparing", "正在收音": "capturing", "正在结束语音": "draining"}[label]
		if report.AudioPhase != wantPhase {
			t.Fatalf("diagnostic phase = %q, want %q", report.AudioPhase, wantPhase)
		}
		if wantPhase == "" {
			if report.AudioSessionID != 0 {
				t.Fatal("idle diagnostic retained a retired session")
			}
		} else if report.AudioSessionID == 0 || report.AudioSessionID != s.coordinator.core.Snapshot().Session.Token.ID() {
			t.Fatal("diagnostic does not identify the current session")
		}
	}
	device, err := bridge.NewDevice("live-host", 1)
	if err != nil {
		t.Fatal(err)
	}
	press := func(id uint64) bridge.PressToken {
		t.Helper()
		p, err := bridge.NewPressToken(device, "voice", id)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	step(bridge.Configure(bridge.NewAudioBinding()))
	step(bridge.RouteAvailable(true))
	step(bridge.ScopeAvailable(true))
	check("已启用")

	first := press(1)
	step(bridge.VoiceBegan(first))
	check("正在准备语音")
	oldToken := s.coordinator.core.Snapshot().Session.Token
	step(bridge.SourcePrepared(oldToken, "first"))
	check("正在收音")
	step(bridge.VoiceEnded(first))
	check("正在结束语音")
	step(bridge.SourceDrained(oldToken))
	check("已启用") // Local drain is not an external recognition/submission ACK.

	early := press(2)
	step(bridge.VoiceBegan(early))
	token := s.coordinator.core.Snapshot().Session.Token
	step(bridge.VoiceEnded(early))
	check("正在结束语音")
	step(bridge.SourcePrepared(token, "early-release"))
	check("正在结束语音") // Late readiness must not visually restart capture.
	step(bridge.SourceDrained(token))
	check("已启用")

	current := press(3)
	step(bridge.VoiceBegan(current))
	token = s.coordinator.core.Snapshot().Session.Token
	step(bridge.SourcePrepared(oldToken, "late-old-source"))
	step(bridge.SourceDrained(oldToken))
	check("正在准备语音")
	if got := s.coordinator.core.Snapshot().Session.Token; got != token {
		t.Fatal("late completion replaced the current session")
	}
	step(bridge.SourcePrepared(token, "current"))
	check("正在收音")
	s.outputNeedsPermission = true
	if label, tone := s.remoteStatus(); label != "需要辅助功能权限" || tone != toneWarning {
		t.Fatal("session presentation hid a permission requirement")
	}
	s.outputNeedsPermission = false
	s.remoteState.stopPending = true
	if label, tone := s.remoteStatus(); label != "正在停用" || tone != toneNeutral {
		t.Fatal("session presentation hid remote shutdown")
	}
	s.remoteState.stopPending = false
	s.liveAudio.backend = compatibilityAudioBackend
	s.report.AudioError = "injected route failure"
	if label, tone := s.remoteStatus(); label != "音频连接失败，请重新连接音频" || tone != toneError {
		t.Fatal("session presentation hid an audio failure")
	}
	s.report.AudioError = ""
	s.liveAudio.backend = ""
	step(bridge.SourceFailed(token))
	step(bridge.VoiceEnded(current))
	check("已启用")

	ordinary := press(4)
	step(bridge.ObservedBegan(ordinary))
	check("已启用") // An observed press alone is not a voice session.
	step(bridge.OrdinaryEnded(ordinary))

	cancelled := press(5)
	step(bridge.VoiceBegan(cancelled))
	token = s.coordinator.core.Snapshot().Session.Token
	step(bridge.RouteAvailable(false))
	step(bridge.SourcePrepared(token, "cancelled-source"))
	step(bridge.VoiceEnded(cancelled))
	step(bridge.RouteAvailable(true))
	check("已启用") // Do not invent an external cancellation/success state.

	mapped := press(6)
	step(bridge.OrdinaryBegan(mapped, "Space"))
	if len(s.coordinator.core.Snapshot().HeldKeys) != 1 {
		t.Fatal("ordinary mapping did not acquire its key lease")
	}
	check("已启用") // A real key-output lease also must not imply voice capture.
	step(bridge.OrdinaryEnded(mapped))
}
