package main

import (
	"context"
	"log"
	"os/exec"
	"time"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/platform/permissionguide"
)

func permissionGuideResetService(id permissionGuideID) string {
	switch id {
	case permissionGuideInput:
		return "ListenEvent"
	case permissionGuidePostEvent:
		return "Accessibility"
	default:
		return ""
	}
}

func permissionGuideCanRecover(id permissionGuideID, snapshot permissionGuideSnapshot) bool {
	access, exists := permissionGuideAccessForID(snapshot, id)
	return permissionGuideResetService(id) != "" && snapshot.actionsEnabled && exists &&
		(access == permissionGuideNotGranted || access == permissionGuideDenied)
}

func (s *shell) recoverPermissionGuideDrop(id permissionGuideID) {
	window, generation := s.guideWindow, s.generation
	if window == nil || window.IsDestroyed() || s.closing || s.recoveryID != id || s.recoveringID != 0 {
		return
	}
	s.refreshPermissionGuide()
	bundleID, ok := permissionguide.MainBundleIdentifier()
	if !ok || !permissionGuideCanRecover(id, s.snapshot) || !permissionGuideBundleIdentityAllowed(bundleID, mygo.App.Name(), s.snapshot.appBundlePath != "") {
		return
	}
	service := permissionGuideResetService(id)
	s.recoveringID = id
	window.Invalidate()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := exec.CommandContext(ctx, "/usr/bin/tccutil", "reset", service, bundleID).Run()
		mygo.RunOnMain(func() {
			if s.guideWindow != window || s.generation != generation || window.IsDestroyed() || s.closing {
				return
			}
			s.recoveringID = 0
			if err != nil {
				log.Printf("permission guide: reset %s record: %v", service, err)
				window.Invalidate()
				return
			}
			s.recoveryID = 0
			s.hasDraggedIcon = false
			s.refreshPermissionGuide()
			s.openPermissionGuideSettings(id, true)
		})
	}()
}
