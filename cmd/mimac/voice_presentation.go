package main

import "mimac.local/mimac/internal/bridge"

// voiceSessionPresentation is a read-only projection, not a second state
// machine. Capture means the local gate opened, not that speech was recognized;
// drain means local teardown, not that another application submitted a result.
type voiceSessionPresentation struct {
	id           uint64
	phase, label string
}

func presentVoiceSession(snapshot bridge.Snapshot) voiceSessionPresentation {
	if !snapshot.HasSession {
		return voiceSessionPresentation{}
	}
	view := voiceSessionPresentation{id: snapshot.Session.Token.ID()}
	switch snapshot.Session.Phase {
	case bridge.PhasePreparing:
		view.phase, view.label = "preparing", "正在准备语音"
	case bridge.PhaseCapturing:
		view.phase, view.label = "capturing", "正在收音"
	case bridge.PhaseDraining:
		view.phase, view.label = "draining", "正在结束语音"
	default:
		return voiceSessionPresentation{}
	}
	return view
}
