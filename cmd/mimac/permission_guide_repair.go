package main

import (
	"context"
	"time"

	"github.com/egoist/mygo"
	"mimac.local/mimac/internal/platform/permissionguide"
)

const permissionGuideRepairHeight = 54

type permissionGuideRepairAttempt struct {
	id       permissionGuideID
	started  time.Time
	checking bool
	stale    bool
}

func permissionGuideMayRepair(snapshot permissionGuideSnapshot, id permissionGuideID) bool {
	access, exists := permissionGuideAccessForID(snapshot, id)
	return snapshot.actionsEnabled && permissionGuideAllowsAppDrag(id) && exists &&
		(access == permissionGuideDenied || access == permissionGuideNotGranted)
}

func permissionGuideRepairPending(snapshot permissionGuideSnapshot, id permissionGuideID) bool {
	access, exists := permissionGuideAccessForID(snapshot, id)
	return snapshot.actionsEnabled && exists && access != permissionGuideGranted && access != permissionGuideRestricted
}

func (s *shell) permissionGuideRepairText(snapshot permissionGuideSnapshot) string {
	attempt := s.repairAttempt
	if attempt == nil || !attempt.stale || !permissionGuideMayRepair(snapshot, attempt.id) {
		return ""
	}
	return permissionGuideRowFor(attempt.id).title + "仍使用旧授权。在系统设置选中「" + mygo.App.Name() + "」，点「−」移除，再把图标拖入。"
}

func (s *shell) recordPermissionGuideDrop(id permissionGuideID, started time.Time) {
	s.repairAttempt = &permissionGuideRepairAttempt{id: id, started: started}
}

// Called after a real drop and again when the app returns to the foreground.
// The usual permission poll remains a read of the capability only.
func (s *shell) checkPermissionGuideRepair() {
	window, attempt := s.guideWindow, s.repairAttempt
	if s.closing || window == nil || window.IsDestroyed() || !window.IsVisible() || attempt == nil ||
		attempt.checking || attempt.stale || !permissionGuideMayRepair(s.snapshot, attempt.id) {
		return
	}
	bundle := s.snapshot.appBundlePath
	service := permissionguide.ListenRegistration
	if attempt.id == permissionGuidePostEvent {
		service = permissionguide.AccessibilityRegistration
	}
	attempt.checking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		stale := permissionguide.HasStaleRegistration(ctx, bundle, service, attempt.started)
		mygo.RunOnMain(func() {
			attempt.checking = false
			if !stale || s.closing || s.guideWindow != window || s.repairAttempt != attempt ||
				window.IsDestroyed() || !window.IsVisible() || !permissionGuideMayRepair(s.snapshot, attempt.id) {
				return
			}
			attempt.stale = true
			window.Invalidate()
		})
	}()
}
