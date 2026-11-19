package main

import (
	"context"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/platform/permissionguide"
)

const (
	permissionGuideRepairTop     float32 = 12
	permissionGuideRepairInset   float32 = 12
	permissionGuideRepairTextGap float32 = 4
)

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

func (s *shell) permissionGuideRepairCopy(snapshot permissionGuideSnapshot) (title, detail string) {
	attempt := s.repairAttempt
	if attempt == nil || !attempt.stale || !permissionGuideMayRepair(snapshot, attempt.id) {
		return "", ""
	}
	return permissionGuideRowFor(attempt.id).title + "授权需更新",
		"在系统设置选中「" + mygo.App.Name() + "」，点「−」移除，再拖入图标"
}

func permissionGuideRepairMeasuredHeight(c *ui.Context, title, detail string) float32 {
	if title == "" {
		return 0
	}
	width := permissionGuideWidth - 2*permissionGuideContentInset - 2*permissionGuideRepairInset
	_, titleHeight := c.MeasureText(width, ui.Span{Text: title, Size: permissionGuideCaption, Weight: 600})
	_, detailHeight := c.MeasureText(width, ui.Span{Text: detail, Size: permissionGuideCaption})
	return permissionGuideRepairTop + titleHeight + permissionGuideRepairTextGap + detailHeight
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
