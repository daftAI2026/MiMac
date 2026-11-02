package main

import "time"

// Opt-in host diagnostics retain the transition that failed, even if normal
// shutdown later clears the live error. No key text, PCM, or device address.
type remoteRecoveryEvent struct {
	At              string `json:"at"`
	Stage           string `json:"stage"`
	Generation      uint64 `json:"generation"`
	OwnerGeneration uint64 `json:"owner_generation"`
	CleanupPending  bool   `json:"cleanup_pending"`
	Error           string `json:"error,omitempty"`
	AudioError      string `json:"audio_error,omitempty"`
}

func (s *shell) recordRemoteRecovery(stage string, generation uint64, err error) {
	if !s.remoteState.reportRecovery {
		return
	}
	event := remoteRecoveryEvent{
		At: time.Now().UTC().Format(time.RFC3339Nano), Stage: stage,
		Generation: generation, OwnerGeneration: s.remoteState.reservationGeneration,
		CleanupPending: s.remoteState.cleanupInFlight, AudioError: s.report.AudioError,
	}
	if err != nil {
		event.Error = err.Error()
	} else if s.remoteState.err != nil {
		event.Error = s.remoteState.err.Error()
	}
	const limit = 64
	if len(s.report.RemoteRecovery) == limit {
		copy(s.report.RemoteRecovery, s.report.RemoteRecovery[1:])
		s.report.RemoteRecovery = s.report.RemoteRecovery[:limit-1]
	}
	s.report.RemoteRecovery = append(s.report.RemoteRecovery, event)
}
