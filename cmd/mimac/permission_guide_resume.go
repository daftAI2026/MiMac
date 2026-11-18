package main

import (
	"errors"
	"log"
	"os"
)

const permissionGuideResumeFile = "permission-resume"

func savePermissionGuideResume(path string, id permissionGuideID) error {
	if !permissionGuideAllowsAppDrag(id) {
		return errors.New("unexpected permission resume target")
	}
	return os.WriteFile(path, []byte(permissionGuideIDString(id)), 0600)
}

func takePermissionGuideResume(path string) (permissionGuideID, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err := os.Remove(path); err != nil {
		return 0, err
	}
	for _, id := range []permissionGuideID{permissionGuideInput, permissionGuidePostEvent} {
		if string(data) == permissionGuideIDString(id) {
			return id, nil
		}
	}
	return 0, nil
}

func shouldResumePermissionGuide(id permissionGuideID, snapshot permissionGuideSnapshot) bool {
	access, exists := permissionGuideAccessForID(snapshot, id)
	return permissionGuideAllowsAppDrag(id) && exists && access == permissionGuideGranted
}

// An orderly OS restart can happen before the running process sees a grant.
// Ordinary Settings keeps a single continuation; initial setup already has
// its own persisted step.
func (s *shell) savePendingPermissionGuide() {
	if s.resumeID == 0 || s.onboarding.permissionGuideOpened || s.guideWindow == nil || !s.guideWindow.IsVisible() {
		return
	}
	path, err := hostDataPath(permissionGuideResumeFile)
	if err == nil {
		err = savePermissionGuideResume(path, s.resumeID)
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
	id, err := takePermissionGuideResume(path)
	if err != nil {
		log.Printf("permission guide resume: %v", err)
		return
	}
	if id != 0 && shouldResumePermissionGuide(id, s.readPermissionGuideSnapshot()) {
		s.page = pageDevice
		s.showSettings()
		s.showPermissionGuide()
	}
}
