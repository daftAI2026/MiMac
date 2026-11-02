package main

import (
	"errors"
	"log"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/adapters/xiaomi"
)

// MyGo owns the native notifications and main thread. Only the host combines
// pause reasons and remembers intent; adapters retain their existing Close.
type powerState struct {
	sleeping, locked            bool
	resumeRemote, resumeMonitor bool
	stopping, resuming          bool
	off                         []func()
}

func (s *shell) watchPower() {
	s.powerState.off = []func(){
		mygo.Power.OnSuspend(func() { s.setPowerSuspended(true) }),
		mygo.Power.OnResume(func() { s.setPowerSuspended(false) }),
		mygo.Power.OnLockScreen(func() { s.setPowerLocked(true) }),
		mygo.Power.OnUnlockScreen(func() { s.setPowerLocked(false) }),
	}
}

func (s *shell) unwatchPower() {
	s.cancelPowerResume()
	for _, off := range s.powerState.off {
		off()
	}
	s.powerState.off = nil
}

func (s *shell) cancelPowerResume() {
	s.powerState.resumeRemote, s.powerState.resumeMonitor = false, false
}

func (s *shell) powerPaused() bool       { return s.powerState.sleeping || s.powerState.locked }
func (s *shell) powerInputBlocked() bool { return s.powerPaused() || s.powerState.stopping }

func (s *shell) setPowerSuspended(value bool) { s.setPowerReason(&s.powerState.sleeping, value) }
func (s *shell) setPowerLocked(value bool)    { s.setPowerReason(&s.powerState.locked, value) }

func (s *shell) setPowerReason(reason *bool, value bool) {
	if s.closing || *reason == value {
		return
	}
	wasPaused := s.powerPaused()
	*reason = value // Close the input gate before cancelling any active session.
	log.Printf("power: sleeping=%t locked=%t cleanup=%t", s.powerState.sleeping, s.powerState.locked, s.powerState.stopping)
	if s.powerPaused() && !wasPaused && !s.powerState.stopping {
		// A user stop already in flight must never become a request to resume.
		if !s.remoteState.stopPending {
			s.powerState.resumeRemote = s.remoteEnabled()
			s.powerState.resumeMonitor = s.monitor != nil && !s.powerState.resumeRemote
		}
		s.cancelRemoteReservationRetry()
		s.endShortcutRecording()
		s.powerState.stopping = s.remoteEnabled() || s.monitor != nil || s.audioSession != nil || s.remoteState.reservation != nil
		if s.coordinator != nil {
			if err := s.coordinator.disconnect(); err != nil {
				s.remoteState.err = err
			}
			s.report.Core = s.coordinator.report()
		}
		s.stopForPowerWhenReady()
	}
	if !s.powerPaused() {
		s.restorePowerIntent()
	}
	s.invalidateDeviceView()
}

// A disconnect or tool swap may already own Close. Its completion resumes
// this stop; never close the same native owner concurrently.
func (s *shell) stopForPowerWhenReady() {
	if s.closing || !s.powerState.stopping || s.plugins == nil ||
		s.remoteState.cleanupInFlight || s.remoteState.stopPending || s.liveAudio.retiring || s.remoteNeedsRestart() {
		return
	}
	s.beginRemoteStop()
}

func (s *shell) finishPowerStop() {
	if !s.powerState.stopping || s.remoteState.err != nil {
		return
	}
	s.powerState.stopping = false
	s.restorePowerIntent()
}

func (s *shell) restorePowerIntent() {
	if s.closing || s.powerInputBlocked() || s.plugins == nil {
		return
	}
	remote, monitor := s.powerState.resumeRemote, s.powerState.resumeMonitor
	s.cancelPowerResume() // One attempt. Missing permission never prompts on wake.
	if s.remoteNeedsRestart() || s.remoteState.err != nil {
		return
	}
	s.powerState.resuming = true
	defer func() { s.powerState.resuming = false }()
	if remote {
		s.enableRemote(false)
	} else if monitor {
		if err := s.plugins.Enable(xiaomiPlugin); err != nil {
			s.remoteState.err = err
			s.deviceNeedsPermission = errors.Is(err, xiaomi.ErrInputMonitoringRequired)
			s.deviceNeedsRestart = errors.Is(err, xiaomi.ErrMonitorRestartRequired) || s.plugins.Failure(xiaomiPlugin) != nil
		}
	}
}
