package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/adapters/xiaomi/atvv"
	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/platform/audioroute"
)

// Resolve the packaged App's data directory on the host thread. Workers can
// append their fixed-code receipts using the path captured for that session.
func audioSessionTracePath() string {
	if !mygo.App.IsPackaged() {
		return ""
	}
	path, _ := hostDataPath("audio-session.log")
	return path
}

// Only fixed stage and result codes leave the audio path. A target error may
// contain text from another application and must not be persisted verbatim.
func traceAudioSession(generation, press, session uint64, event string, err error) {
	if path := audioSessionTracePath(); path != "" {
		appendPrivateTrace(path, audioTraceRecord(time.Now(), os.Getpid(), generation, press, session, event, err))
	}
}

// Write the accepted binding with the existing press receipt. It is the plan
// adopted by this particular gesture, even if the user later changes tools.
func traceVoiceEdge(press bridge.PressToken, state bridge.Snapshot, event string, err error) {
	path := audioSessionTracePath()
	if path == "" {
		return
	}
	record := audioTraceRecord(time.Now(), os.Getpid(), press.Device().Generation(), press.PressID(), state.Session.Token.ID(), event, err)
	if event == "press" && state.HasSession && state.Session.Press == press {
		switch state.Session.Binding.Policy() {
		case bridge.PolicyExactEdge:
			record += " end_policy=release_edge"
		case bridge.PolicyAfterDrain:
			record += " end_policy=release_after_drain"
		case bridge.PolicyToggle:
			record += " end_policy=toggle"
		case bridge.PolicyAudio:
			record += " end_policy=audio_only"
		}
	}
	appendPrivateTrace(path, record)
}

func audioTraceRecord(at time.Time, pid int, generation, press, session uint64, event string, err error) string {
	return fmt.Sprintf("time=%s pid=%d generation=%d press=%d session=%d event=%s stage=%s result=%s",
		at.UTC().Format(time.RFC3339Nano), pid, generation, press, session,
		event, audiosession.FailureStage(err), audioTraceResult(err))
}

func audioTraceResult(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, errCompatRouteTargetBusy):
		return "target_busy"
	case errors.Is(err, errCompatReceiverStopped):
		return "target_stopped"
	case errors.Is(err, errCompatRouteTargetMissing):
		return "target_missing"
	case errors.Is(err, audioroute.ErrInputRouteMismatch):
		return "wrong_input"
	case errors.Is(err, atvv.ErrTransportStopTimeout):
		return "source_stop_timeout"
	case errors.Is(err, atvv.ErrRemoteNotConnected):
		return "remote_disconnected"
	case errors.Is(err, atvv.ErrUnexpectedStream):
		return "source_second_start"
	case errors.Is(err, atvv.ErrAudioBeforeStart):
		return "source_audio_before_start"
	case errors.Is(err, atvv.ErrAudioAfterStop):
		return "source_audio_after_stop"
	case errors.Is(err, atvv.ErrUnexpectedStop):
		return "source_stop_without_active_stream"
	case errors.Is(err, atvv.ErrTransportFault):
		return "source_transport"
	case errors.Is(err, atvv.ErrSourceQueueFull):
		return "source_overflow"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "error"
	}
}
