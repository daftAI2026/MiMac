package main

import (
	"errors"
	"log"
	"math"
	"time"

	"mimac.local/mimac/internal/audiometer"
	"mimac.local/mimac/internal/bridge"
)

// This is a render snapshot. Core and the existing PCM owner remain the only
// sources of session identity, phase and input level.
type voiceOverlayFrame struct {
	Visible  bool
	Token    bridge.SessionToken
	Label    string
	DB       float32
	Spectrum audiometer.Spectrum
}

type voiceOverlayView interface {
	Update(voiceOverlayFrame) error
	Hide() error
	Close() error
}

type voiceOverlayState struct {
	open  func(func() voiceOverlayFrame, func(error)) (voiceOverlayView, error)
	view  voiceOverlayView
	err   error // Optional display failure, never an audio/core failure.
	meter voiceSpectrumState
}

func (s *shell) voiceOverlayFrameAt(now time.Time) voiceOverlayFrame {
	frame := voiceOverlayFrame{DB: -60}
	if !s.preferencesState.value.ShowVoiceOverlay || s.preferencesState.loadFailed || s.closing ||
		s.powerInputBlocked() || s.remoteState.stopPending || s.liveAudio.retiring || s.coordinator == nil ||
		s.audioSession == nil || s.audioSession.runtime == nil {
		return frame
	}
	snapshot := s.coordinator.core.Snapshot()
	if !snapshot.HasSession {
		return frame
	}
	phase := presentVoiceSession(snapshot)
	if phase.phase == "" {
		return frame
	}
	frame.Visible, frame.Token, frame.Label = true, snapshot.Session.Token, phase.label
	if snapshot.Session.Phase == bridge.PhaseCapturing || snapshot.Session.Phase == bridge.PhaseDraining {
		frame.Spectrum = s.voiceOverlay.meter.snapshot(frame.Token, now)
	}
	level := presentVoiceInput(snapshot, s.coordinator.audio)
	if level == nil || level.RMS <= 0 || math.IsNaN(float64(level.RMS)) || math.IsInf(float64(level.RMS), 0) ||
		now.Before(level.ObservedAt) || now.Sub(level.ObservedAt) > 100*time.Millisecond {
		return frame
	}
	frame.DB = float32(max(-60, min(0, 20*math.Log10(float64(level.RMS)))))
	return frame
}

func (s *shell) syncVoiceOverlay() {
	state := &s.voiceOverlay
	state.meter.setEnabled(!s.closing && s.preferencesState.value.ShowVoiceOverlay && !s.preferencesState.loadFailed && state.err == nil)
	if s.closing || !s.preferencesState.value.ShowVoiceOverlay {
		s.closeVoiceOverlay()
		return
	}
	if state.err != nil || state.open == nil {
		return
	}
	frame := s.voiceOverlayFrameAt(time.Now())
	if !frame.Visible {
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

func (s *shell) closeVoiceOverlay() { s.recordVoiceOverlayError(s.releaseVoiceOverlay()) }
