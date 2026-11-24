package main

import (
	"errors"
	"log"
	"math"
	"time"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/platform/siriglass"
	"mimac.local/mimac/internal/pluginrt"
)

// This is a render snapshot. Core and the existing PCM owner remain the only
// sources of session identity, phase and input level.
type voiceOverlayFrame struct {
	Visible          bool
	Token            bridge.SessionToken
	Phase            bridge.Phase
	Label            string
	DB               float32
	CenteredRMS      float32
	Spectrum         siriglass.Spectrum
	Input            siriglass.InputState
	DotAtBottom      bool
	ShowLiveText     bool
	InterimStable    string
	InterimTentative string
}

type voiceOverlayView interface {
	Update(voiceOverlayFrame) error
	Hide() error
	Close() error
}

type voiceOverlayState struct {
	open         func(func() voiceOverlayFrame, func(error)) (voiceOverlayView, error)
	view         voiceOverlayView
	err          error // Optional display failure, never an audio/core failure.
	meter        voiceSpectrumState
	mode         voicePresentationMode
	interim      voiceInterimFeed
	interimToken bridge.SessionToken // Main-thread owner for Begin/End; provider calls use the feed mutex.
	voicePrompt  voicePromptPresentation
}

const voicePresentationPlugin = "voice-presentation"

func (s *shell) voicePresentationManifest() pluginrt.Manifest {
	return pluginrt.Manifest{ID: voicePresentationPlugin, Start: func() (pluginrt.StopFunc, error) {
		s.voiceOverlay.meter.setEnabled(s.voiceOverlay.err == nil && s.voiceOverlay.mode == voicePresentationMiMacSphere)
		return func() error {
			s.voiceOverlay.meter.setEnabled(false)
			return s.releaseVoiceOverlay()
		}, nil
	}}
}

func (s *shell) voicePresentationActive() bool {
	return s.plugins != nil && s.plugins.Active(voicePresentationPlugin)
}

func (s *shell) applyVoicePresentationSelection() {
	s.configureVoiceInterim()
	if s.plugins == nil {
		return
	}
	mode := s.preferencesState.value.VoicePresentation
	if mode != s.voiceOverlay.mode {
		// A healthy local-to-local swap keeps the already leased target prompt
		// hidden for this same Core session. Other transitions retire that lease.
		localToLocal := isLocalVoicePresentation(s.voiceOverlay.mode) && isLocalVoicePresentation(mode) &&
			s.voicePresentationActive() && s.voiceOverlay.err == nil && !s.closing && !s.preferencesState.loadFailed
		var err error
		if localToLocal {
			err = s.releaseVoiceOverlayView()
		} else {
			err = s.releaseVoiceOverlay()
		}
		if err != nil {
			// A failed replacement cannot claim that the local surface still
			// covers the target prompt.
			s.stopVoicePromptPresentation()
			s.recordVoiceOverlayError(err)
			return
		}
		s.voiceOverlay.mode = mode
		s.voiceOverlay.err = nil
		s.report.VoiceOverlayError = ""
	}
	local := isLocalVoicePresentation(mode)
	if !local || s.preferencesState.loadFailed || s.closing {
		s.stopVoicePresentation()
		return
	}
	if !s.voicePresentationActive() {
		s.recordVoiceOverlayError(s.plugins.Enable(voicePresentationPlugin))
	} else {
		s.voiceOverlay.meter.setEnabled(s.voiceOverlay.err == nil && mode == voicePresentationMiMacSphere)
	}
	s.syncVoiceOverlay()
}

func isLocalVoicePresentation(mode voicePresentationMode) bool {
	return mode == voicePresentationMiMacSphere || mode == voicePresentationDotBar
}

func (s *shell) stopVoicePresentation() {
	s.stopVoicePromptPresentation()
	s.endVoiceInterim()
	s.voiceOverlay.interim.SetEnabled(false)
	if s.voicePresentationActive() {
		s.recordVoiceOverlayError(s.plugins.Disable(voicePresentationPlugin))
	}
}

func (s *shell) configureVoiceInterim() {
	state := &s.voiceOverlay
	enabled := !s.closing && !s.preferencesState.loadFailed &&
		s.preferencesState.value.ShowLiveText && s.preferencesState.value.VoicePresentation == voicePresentationDotBar
	state.interim.SetEnabled(enabled)
	if !enabled {
		state.interimToken = bridge.SessionToken{}
	}
}

func (s *shell) beginVoiceInterim(token bridge.SessionToken) {
	state := &s.voiceOverlay
	if state.interimToken != token {
		state.interim.End(state.interimToken)
		state.interim.Begin(token)
		state.interimToken = token
	}
}

func (s *shell) endVoiceInterim() {
	state := &s.voiceOverlay
	state.interim.End(state.interimToken)
	state.interimToken = bridge.SessionToken{}
}

func (s *shell) voiceOverlayFrameAt(now time.Time) voiceOverlayFrame {
	frame := voiceOverlayFrame{DB: -60, DotAtBottom: s.preferencesState.value.DotAtBottom}
	frame.ShowLiveText = !s.closing && !s.preferencesState.loadFailed &&
		s.preferencesState.value.ShowLiveText && s.preferencesState.value.VoicePresentation == voicePresentationDotBar
	if !s.voicePresentationActive() || s.voiceOverlay.err != nil || s.closing ||
		s.powerInputBlocked() || s.remoteState.stopPending || s.liveAudio.retiring || s.coordinator == nil ||
		s.audioSession == nil || s.audioSession.runtime == nil {
		s.endVoiceInterim()
		return frame
	}
	snapshot := s.coordinator.core.Snapshot()
	if !snapshot.HasSession {
		s.endVoiceInterim()
		return frame
	}
	phase := presentVoiceSession(snapshot)
	if phase.phase == "" {
		s.endVoiceInterim()
		return frame
	}
	if frame.ShowLiveText {
		s.beginVoiceInterim(snapshot.Session.Token)
		draft := s.voiceOverlay.interim.Snapshot(snapshot.Session.Token)
		frame.InterimStable, frame.InterimTentative = draft.Stable, draft.Tentative
	} else {
		s.endVoiceInterim()
	}
	frame.Visible, frame.Token, frame.Label = true, snapshot.Session.Token, phase.label
	frame.Phase = snapshot.Session.Phase
	frame.Input = siriglass.InputState{
		IsRecording:            snapshot.Session.Phase == bridge.PhaseCapturing,
		HasVoiceInputOwnership: snapshot.HasSession,
	}
	if s.voiceOverlay.mode == voicePresentationMiMacSphere && (frame.Phase == bridge.PhaseCapturing || frame.Phase == bridge.PhaseDraining) {
		frame.Spectrum = s.voiceOverlay.meter.snapshot(frame.Token, now)
	}
	level := presentVoiceInput(snapshot, s.coordinator.audio)
	if level == nil || level.RMS < 0 || math.IsNaN(float64(level.RMS)) || math.IsInf(float64(level.RMS), 0) ||
		now.Before(level.ObservedAt) || now.Sub(level.ObservedAt) > 100*time.Millisecond {
		return frame
	}
	if s.voiceOverlay.mode == voicePresentationDotBar {
		frame.CenteredRMS = level.CenteredRMS
	} else if level.RMS > 0 {
		frame.DB = float32(max(-60, min(0, 20*math.Log10(float64(level.RMS)))))
	}
	return frame
}

func (s *shell) syncVoiceOverlay() {
	state := &s.voiceOverlay
	if s.closing || !s.voicePresentationActive() || state.err != nil || state.open == nil {
		s.stopVoicePromptPresentation()
		return
	}
	frame := s.voiceOverlayFrameAt(time.Now())
	if !frame.Visible {
		// Normal session end still belongs to the target presentation adapter.
		s.syncVoicePromptPresentation(frame)
		if state.view != nil {
			if err := state.view.Hide(); err != nil {
				s.failVoiceOverlay(err)
			}
		}
		return
	}
	if state.view == nil {
		view, err := state.open(func() voiceOverlayFrame { return s.voiceOverlayFrameAt(time.Now()) }, s.failVoiceOverlay)
		// Even a failed constructor may own a native handle needing cleanup.
		state.view = view
		if err == nil && view == nil {
			err = errors.New("voice overlay: no renderer returned")
		}
		if err != nil {
			s.failVoiceOverlay(err)
			return
		}
	}
	if err := state.view.Update(frame); err != nil {
		s.failVoiceOverlay(err)
		return
	}
	// Keep the target's recognition-window discovery independent from input
	// delivery, and don't suppress its prompt unless the local surface for
	// this same frame actually rendered successfully.
	s.syncVoicePromptPresentation(frame)
}

// Report an explicit outcome only to renderers that can present it.
func (s *shell) finishVoiceOverlay(token bridge.SessionToken, failed bool) {
	s.finishVoicePromptPresentation(token)
	if view, ok := s.voiceOverlay.view.(interface {
		finish(bridge.SessionToken, bool, time.Time)
	}); ok {
		view.finish(token, failed, time.Now())
	}
}

func (s *shell) failVoiceOverlay(err error) {
	if err == nil {
		return
	}
	s.recordVoiceOverlayError(errors.Join(err, s.releaseVoiceOverlay()))
}

func (s *shell) recordVoiceOverlayError(err error) {
	if err == nil {
		return
	}
	if s.voiceOverlay.err == nil {
		log.Printf("voice overlay unavailable: %v", err)
	}
	s.voiceOverlay.err = err
	s.voiceOverlay.meter.setEnabled(false)
	s.report.VoiceOverlayError = err.Error()
}

func (s *shell) releaseVoiceOverlay() error {
	s.stopVoicePromptPresentation()
	return s.releaseVoiceOverlayView()
}

func (s *shell) releaseVoiceOverlayView() error {
	s.endVoiceInterim()
	s.voiceOverlay.interim.Clear()
	view := s.voiceOverlay.view
	if view == nil {
		return nil
	}
	hideErr := view.Hide() // Stop presentation before attempting native cleanup.
	closeErr := view.Close()
	if closeErr == nil {
		s.voiceOverlay.view = nil
	}
	return errors.Join(hideErr, closeErr)
}

func (s *shell) closeVoiceOverlay() {
	s.recordVoiceOverlayError(s.releaseVoiceOverlay())
	s.waitVoicePromptPresentationRestore()
}
