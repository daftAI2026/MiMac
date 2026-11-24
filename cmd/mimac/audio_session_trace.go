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
	"mimac.local/mimac/internal/platform/audioroute"
)

// Called on the host thread at control boundaries, never per PCM block.
// Only fixed stage and result codes leave the audio path. A target error may
// contain text from another application and must not be persisted verbatim.
func traceAudioSession(generation, press, session uint64, event string, err error) {
	if !mygo.App.IsPackaged() {
		return
	}
	path, pathErr := hostDataPath("audio-session.log")
	if pathErr != nil {
		return
	}
	appendPrivateTrace(path, audioTraceRecord(time.Now(), os.Getpid(), generation, press, session, event, err))
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
	case errors.Is(err, atvv.ErrAudioBeforeStart), errors.Is(err, atvv.ErrAudioAfterStop), errors.Is(err, atvv.ErrUnexpectedStop):
		return "source_event_order"
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
