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

func (s *shell) canOfferPermissionRepair(row permissionGuideRow) bool {
	_, allowed := permissionGuideResetService(row.id, row.access)
	return allowed && s.repairOffered[row.id] && !s.repairAttempted[row.id]
}

// Called only from an explicitly labeled recovery control. The fresh probe
// guards against a grant received between drawing and clicking that control.
func (s *shell) repairPermissionGuideAccess(id permissionGuideID) {
	window := s.guideWindow
	if s.closing || s.repairing || window == nil || window.IsDestroyed() {
		return
	}
	fresh := s.readPermissionGuideSnapshot()
	access, exists := permissionGuideAccessForID(fresh, id)
	service, resettable := permissionGuideResetService(id, access)
	bundleID, identityOK := permissionguide.MainBundleIdentifier()
	if !exists || !resettable || !fresh.actionsEnabled ||
		fresh.appBundlePath != s.snapshot.appBundlePath ||
		!identityOK || !permissionGuideBundleIdentityAllowed(bundleID, mygo.App.Name(), true) ||
		!s.canOfferPermissionRepair(permissionGuideRow{id: id, access: access}) {
		s.refreshPermissionGuide()
		return
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
				window.Invalidate()
				return
			}
			s.refreshPermissionGuide()
			if access, exists := permissionGuideAccessForID(s.snapshot, id); exists && access != permissionGuideGranted {
				s.openPermissionGuideSettings(id, true)
			}
		})
	}()
}

func (s *shell) yieldPermissionGuideKeyboard() bool {
	window := s.guideWindow
	if window == nil || window.IsDestroyed() || !window.IsVisible() {
		return false
	}
	permissionguide.YieldToSystemSettings()
	if !window.IsFocused() {
		return false
	}
	window.Hide()
	window.ShowInactive()
	return true
}
