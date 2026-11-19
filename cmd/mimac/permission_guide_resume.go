package main

import (
	"errors"
	"log"
	"os"
	"strings"
)

const permissionGuideResumeFile = "permission-resume"

type permissionGuideContinuation struct {
	id         permissionGuideID
	onboarding bool
}

func savePermissionGuideResume(path string, continuation permissionGuideContinuation) error {
	if !permissionGuideAllowsAppDrag(continuation.id) {
		return errors.New("unexpected permission resume target")
	}
	token := permissionGuideIDString(continuation.id)
	if continuation.onboarding {
		token = "onboarding:" + token
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
	token, onboarding := strings.CutPrefix(string(data), "onboarding:")
	for _, id := range []permissionGuideID{permissionGuideInput, permissionGuidePostEvent} {
		if token == permissionGuideIDString(id) {
			return permissionGuideContinuation{id: id, onboarding: onboarding}, nil
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
	if s.resumeID == 0 || s.guideWindow == nil || !s.guideWindow.IsVisible() {
		return
	}
	path, err := hostDataPath(permissionGuideResumeFile)
	if err == nil {
		err = savePermissionGuideResume(path, permissionGuideContinuation{
			id: s.resumeID, onboarding: s.onboarding.permissionGuideOpened,
		})
	}
	if err != nil {
		log.Printf("permission guide resume: %v", err)
	}
}

func (s *shell) resumePermissionGuideOnLaunch() {
	path, err := hostDataPath(permissionGuideResumeFile)
	if err != nil {
		return
	}
	continuation, err := takePermissionGuideResume(path)
	if err != nil {
		log.Printf("permission guide resume: %v", err)
		return
	}
	if continuation.id == 0 {
		return
	}
	target := permissionGuideResumeTarget(continuation, s.onboarding.window != nil, s.onboarding.step, s.readPermissionGuideSnapshot())
	if target == 0 {
		return
	}
	if continuation.onboarding {
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
