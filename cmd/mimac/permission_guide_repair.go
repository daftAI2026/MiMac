package main

import (
	"context"
	"log"
	"os/exec"
	"time"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/platform/permissionguide"
)

func permissionGuideResetService(id permissionGuideID, access permissionGuideAccess) (string, bool) {
	switch {
	case id == permissionGuideInput && access == permissionGuideDenied:
		return "ListenEvent", true
	case id == permissionGuidePostEvent && access == permissionGuideNotGranted:
		return "Accessibility", true
	default:
		return "", false
	}
}

func (s *shell) permissionGuideGrantNeedsRepair(id permissionGuideID, access permissionGuideAccess) bool {
	_, allowed := permissionGuideResetService(id, access)
	return allowed && !s.repairAttempted[id]
}

// Only a deliberate row action may recover an old record. Refresh first
// so a grant received since drawing the row is never reset.
func (s *shell) preparePermissionGuideGrant(id permissionGuideID) bool {
	window := s.guideWindow
	if s.closing || s.repairing || window == nil || window.IsDestroyed() {
		return true
	}
	fresh := s.readPermissionGuideSnapshot()
	access, exists := permissionGuideAccessForID(fresh, id)
	service, resettable := permissionGuideResetService(id, access)
	bundleID, identityOK := permissionguide.MainBundleIdentifier()
	if !exists || access == permissionGuideGranted || access == permissionGuideRestricted {
		s.refreshPermissionGuide()
		return true
	}
	if !resettable || !fresh.actionsEnabled ||
		fresh.appBundlePath != s.snapshot.appBundlePath ||
		!identityOK || !permissionGuideBundleIdentityAllowed(bundleID, mygo.App.Name(), true) ||
		!s.permissionGuideGrantNeedsRepair(id, access) {
		return false
	}
	s.repairAttempted[id] = true
	s.repairing = true
	s.currentID = id
	s.repairFailedFor = 0
	generation := s.generation
	window.Invalidate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := exec.CommandContext(ctx, "/usr/bin/tccutil", "reset", service, bundleID).Run()
		mygo.RunOnMain(func() {
			s.repairing = false
			if s.closing || s.guideWindow != window || s.generation != generation || window.IsDestroyed() {
				return
			}
			if err != nil {
				log.Printf("permission recovery: %s: %v", service, err)
				s.repairFailedFor = id
			}
			s.refreshPermissionGuide()
			if access, exists := permissionGuideAccessForID(s.snapshot, id); exists && access != permissionGuideGranted {
				s.openPermissionGuideSettings(id, true)
			}
		})
	}()
	return true
}
