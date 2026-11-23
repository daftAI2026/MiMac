package main

import (
	"time"

	"mimac.local/mimac/internal/platform/permissionguide"
)

func permissionGuideTimerCurrent(token, current uint64) bool {
	return token == current
}

func (s *shell) onboardingPermissionSurfaceVisible() bool {
	window := s.onboarding.window
	return window != nil && !window.IsDestroyed() && window.IsVisible() && s.onboarding.step == onboardingStepPermissions
}

func (s *shell) permissionGuideRefreshSurfaceVisible() bool {
	window := s.guideWindow
	return (window != nil && !window.IsDestroyed() && window.IsVisible()) || s.onboardingPermissionSurfaceVisible()
}

func (s *shell) permissionGuideInteractionActive() bool {
	return s.iconDragging || (s.dropGuide != nil && s.dropGuide.active)
}

func (s *shell) publishPermissionGuideSnapshot(next permissionGuideSnapshot) (guideChanged, onboardingChanged bool) {
	for _, change := range permissionGuideAccessTransitions(s.snapshot, next) {
		permissionGuideTrace(permissionGuideTracePermissionChange, change.id, permissionGuideTraceChanged, change.to, change.from)
	}
	guideChanged = !samePermissionGuideSnapshot(s.snapshot, next)
	s.snapshot = next
	if s.onboarding.step == onboardingStepPermissions {
		s.onboarding.permissionSnapshot = next
		onboardingChanged = guideChanged
		if onboardingChanged && s.onboardingPermissionSurfaceVisible() {
			s.onboarding.window.Invalidate()
		}
	}
	return guideChanged, onboardingChanged
}

func (s *shell) refreshPermissionGuideSnapshot() bool {
	if s.closing {
		return false
	}
	window := s.guideWindow
	if window == nil || window.IsDestroyed() || !window.IsVisible() {
		if !s.onboardingPermissionSurfaceVisible() {
			return false
		}
		next := s.readPermissionGuideSnapshot()
		s.publishPermissionGuideSnapshot(next)
		s.finishPermissionGuideNativeRequest(next)
		if !permissionGuideHasPendingAuthorization(next) {
			s.stopPermissionGuideReadback()
		}
		return true
	}

	previous := s.snapshot
	next := s.readPermissionGuideSnapshot()
	closeOnboardingGuide, finishSettingsVisit := s.onboarding.completePermissionVisit(previous, next)
	previousHeight := permissionGuideWindowHeight(previous)
	changed, _ := s.publishPermissionGuideSnapshot(next)
	s.finishPermissionGuideNativeRequest(next)
	if attempt := s.repairAttempt; attempt != nil && !permissionGuideRepairPending(next, attempt.id) {
		s.repairAttempt = nil
	}
	if nextHeight := permissionGuideWindowHeight(next); nextHeight != previousHeight {
		window.SetSize(permissionGuideWidth, nextHeight)
	}
	if changed && s.generalControls != nil {
		s.syncGeneralControls()
	}
	if closeOnboardingGuide {
		s.closePermissionGuide()
		if finishSettingsVisit {
			permissionguide.FinishSystemSettingsVisit()
		}
		return true
	}
	if changed && s.followActive && permissionGuideHasPendingAuthorization(next) {
		s.armPermissionGuideReadback(false)
	}
	if !permissionGuideHasPendingAuthorization(next) {
		s.stopPermissionGuideReadback()
	}
	stateChanged := s.advancePermissionGuide()
	if changed || stateChanged {
		window.Invalidate()
	}
	return true
}

func (s *shell) schedulePermissionGuideReadback() {
	if s.followActive || s.settingsOpenedFor != 0 || s.permissionGuideNativeRequest {
		s.armPermissionGuideReadback(s.permissionGuideNativeRequest)
		if window := s.guideWindow; window != nil && !window.IsDestroyed() && window.IsVisible() {
			s.schedulePermissionGuidePoll(window, s.generation)
		}
	}
}

func (s *shell) stopPermissionGuideReadback() {
	s.readbackUntil = time.Time{}
	s.permissionGuideNativeRequest = false
}

func permissionGuideHasPendingAuthorization(snapshot permissionGuideSnapshot) bool {
	for _, row := range snapshot.rows {
		if row.access != permissionGuideGranted && row.access != permissionGuideRestricted {
			return true
		}
	}
	return false
}

func (s *shell) armPermissionGuideReadback(nativeRequest bool) {
	if s.closing || !s.permissionGuideRefreshSurfaceVisible() || !permissionGuideHasPendingAuthorization(s.snapshot) {
		return
	}
	until := time.Now().Add(permissionGuideReadbackDuration)
	if until.After(s.readbackUntil) {
		s.readbackUntil = until
	}
	if nativeRequest {
		s.permissionGuideNativeRequest = true
	}
}

func (s *shell) finishPermissionGuideNativeRequest(snapshot permissionGuideSnapshot) {
	if !s.permissionGuideNativeRequest {
		return
	}
	access, exists := permissionGuideAccessForID(snapshot, permissionGuideBluetooth)
	if !exists || (access != permissionGuideNotDetermined && access != permissionGuideUnknown) {
		s.permissionGuideNativeRequest = false
		if !s.followActive {
			s.stopPermissionGuideReadback()
		}
	}
}

func (s *shell) permissionGuidePollIntervalAt(now time.Time) time.Duration {
	if now.Before(s.readbackUntil) && permissionGuideHasPendingAuthorization(s.snapshot) {
		return permissionGuideReadbackInterval
	}
	return permissionGuidePollInterval
}
