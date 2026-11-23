package main

import (
	"errors"
	"log"
	"os"
	"strings"

	"mimac.local/mimac/internal/platform/permissionguide"
)

const permissionGuideResumeFile = "permission-resume"

type permissionGuideContinuation struct {
	id          permissionGuideID
	onboarding  bool
	finishVisit bool
}

func savePermissionGuideResume(path string, continuation permissionGuideContinuation) error {
	if !permissionGuideAllowsAppDrag(continuation.id) {
		return errors.New("unexpected permission resume target")
	}
	token := permissionGuideIDString(continuation.id)
	if continuation.onboarding {
		if continuation.finishVisit {
			token = "onboarding-visit:" + token
		} else {
			token = "onboarding:" + token
		}
	}
	return os.WriteFile(path, []byte(token), 0600)
}

func takePermissionGuideResume(path string) (permissionGuideContinuation, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return permissionGuideContinuation{}, nil
	}
	if err != nil {
		return permissionGuideContinuation{}, err
	}
	if err := os.Remove(path); err != nil {
		return permissionGuideContinuation{}, err
	}
	token, finishVisit := strings.CutPrefix(string(data), "onboarding-visit:")
	token, onboarding := strings.CutPrefix(token, "onboarding:")
	token, _, _ = strings.Cut(token, "@") // Older builds included a drop timestamp.
	for _, id := range []permissionGuideID{permissionGuideInput, permissionGuidePostEvent} {
		if token == permissionGuideIDString(id) {
			return permissionGuideContinuation{id: id, onboarding: onboarding || finishVisit, finishVisit: finishVisit}, nil
		}
	}
	return permissionGuideContinuation{}, nil
}

func permissionGuideCanRestore(id permissionGuideID, snapshot permissionGuideSnapshot) bool {
	_, exists := permissionGuideAccessForID(snapshot, id)
	return permissionGuideAllowsAppDrag(id) && exists
}

// An orderly OS restart can happen before the running process sees a grant.
// Save the guide independently of the onboarding page's existing progress.
func (s *shell) savePendingPermissionGuide() {
	id := s.resumeID
	if id == 0 || s.guideWindow == nil || !s.guideWindow.IsVisible() {
		permissionGuideTrace(permissionGuideTraceResumeSave, id, permissionGuideTraceSkipped, permissionGuideUnknown, permissionGuideUnknown)
		return
	}
	path, err := hostDataPath(permissionGuideResumeFile)
	if err != nil {
		log.Printf("permission guide resume: %v", err)
		permissionGuideTrace(permissionGuideTraceResumeSave, id, permissionGuideTraceError, permissionGuideUnknown, permissionGuideUnknown)
		return
	}
	continuation := permissionGuideContinuation{
		id:          id,
		onboarding:  s.onboarding.permissionGuideOpened,
		finishVisit: s.onboarding.permissionGuideOpened && s.onboarding.permissionActionTaken,
	}
	err = savePermissionGuideResume(path, continuation)
	if err != nil {
		log.Printf("permission guide resume: %v", err)
		permissionGuideTrace(permissionGuideTraceResumeSave, id, permissionGuideTraceError, permissionGuideUnknown, permissionGuideUnknown)
		return
	}
	permissionGuideTrace(permissionGuideTraceResumeSave, id, permissionGuideTraceSaved, permissionGuideUnknown, permissionGuideUnknown)
}

func readPermissionGuideResumeOnLaunch() permissionGuideContinuation {
	path, err := hostDataPath(permissionGuideResumeFile)
	if err != nil {
		permissionGuideTrace(permissionGuideTraceResumeConsume, 0, permissionGuideTraceError, permissionGuideUnknown, permissionGuideUnknown)
		return permissionGuideContinuation{}
	}
	continuation, err := takePermissionGuideResume(path)
	if err != nil {
		log.Printf("permission guide resume: %v", err)
		permissionGuideTrace(permissionGuideTraceResumeConsume, 0, permissionGuideTraceError, permissionGuideUnknown, permissionGuideUnknown)
		return permissionGuideContinuation{}
	}
	if continuation.id == 0 {
		permissionGuideTrace(permissionGuideTraceResumeConsume, 0, permissionGuideTraceEmpty, permissionGuideUnknown, permissionGuideUnknown)
	} else {
		permissionGuideTrace(permissionGuideTraceResumeConsume, continuation.id, permissionGuideTraceConsumed, permissionGuideUnknown, permissionGuideUnknown)
	}
	return continuation
}

func (s *shell) resumePermissionGuideOnLaunch(continuation permissionGuideContinuation) {
	if continuation.id == 0 {
		return
	}
	snapshot := s.readPermissionGuideSnapshot()
	s.publishPermissionGuideSnapshot(snapshot)
	onboardingVisible := s.onboarding.window != nil
	target := permissionGuideResumeTarget(continuation, onboardingVisible, s.onboarding.step, snapshot)
	finishVisit := permissionGuideResumesOnboardingVisit(continuation, onboardingVisible, s.onboarding.step, snapshot)
	status, _ := permissionGuideAccessForID(snapshot, continuation.id)
	restoreResult := permissionGuideTraceNotRestored
	if target != 0 {
		restoreResult = permissionGuideTraceRestored
	}
	permissionGuideTrace(permissionGuideTraceResumeRestore, continuation.id, restoreResult, status, permissionGuideUnknown)
	if target == 0 {
		if finishVisit && summarizeOnboardingPermissions(snapshot.rows).allGranted() {
			permissionguide.FinishSystemSettingsVisit()
		}
		return
	}
	if continuation.onboarding {
		s.onboarding.permissionActionTaken = finishVisit
		s.showOnboardingPermissionGuideFor(target)
	} else {
		s.page = pageGeneral
		s.showSettings()
		s.showPermissionGuideFor(target)
	}
}

func permissionGuideResumeTarget(continuation permissionGuideContinuation, onboardingVisible bool, step onboardingStep, snapshot permissionGuideSnapshot) permissionGuideID {
	if continuation.onboarding != onboardingVisible || !permissionGuideCanRestore(continuation.id, snapshot) {
		return 0
	}
	if !onboardingVisible {
		return continuation.id
	}
	if step != onboardingStepPermissions {
		return 0
	}
	// If the system already granted the last permission, keep setup on its
	// current page with the completed summary and the explicit Continue.
	return nextPermissionGuideTarget(snapshot, 0, continuation.id)
}

func permissionGuideResumesOnboardingVisit(continuation permissionGuideContinuation, visible bool, step onboardingStep, snapshot permissionGuideSnapshot) bool {
	return continuation.onboarding && continuation.finishVisit && visible &&
		step == onboardingStepPermissions && permissionGuideCanRestore(continuation.id, snapshot)
}

// Create the main onboarding window without an activating first frame while
// the resumed guide is about to route an existing Settings session.
func permissionGuideResumesWithSettings(continuation permissionGuideContinuation, step onboardingStep, snapshot permissionGuideSnapshot, settingsRunning func() bool) bool {
	target := permissionGuideResumeTarget(continuation, true, step, snapshot)
	access, exists := permissionGuideAccessForID(snapshot, target)
	return snapshot.actionsEnabled && exists && permissionGuideCanRoute(target, access) && settingsRunning()
}
