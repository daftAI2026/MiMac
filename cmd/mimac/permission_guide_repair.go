package main

import (
	"context"
	"log"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/adapters/textoutput"
	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/platform/permissionguide"
)

const (
	permissionGuideRepairTop     float32 = 12
	permissionGuideRepairInset   float32 = 12
	permissionGuideRepairTextGap float32 = 4
)

type permissionGuideRepairAttempt struct {
	id             permissionGuideID
	started        time.Time
	checking       bool
	stale          bool
	resetAttempted bool
	resetDone      bool
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
	if attempt.resetDone {
		return permissionGuideRowFor(attempt.id).title + "等待系统授权", "请在系统设置完成授权"
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
	if attempt := s.repairAttempt; attempt != nil && attempt.id == id && attempt.resetAttempted && attempt.checking {
		return
	}
	s.repairAttempt = &permissionGuideRepairAttempt{id: id, started: started}
}

func (attempt *permissionGuideRepairAttempt) takeReset(snapshot permissionGuideSnapshot) bool {
	if !attempt.stale || attempt.resetAttempted || !permissionGuideMayRepair(snapshot, attempt.id) {
		return false
	}
	attempt.resetAttempted = true
	return true
}

// Called after a real drop and again when the app returns to the foreground.
// The usual permission poll remains a read of the capability only.
func (s *shell) checkPermissionGuideRepair() {
	window, attempt := s.guideWindow, s.repairAttempt
	if s.closing || window == nil || window.IsDestroyed() || !window.IsVisible() || attempt == nil ||
		attempt.checking || attempt.resetAttempted || !permissionGuideMayRepair(s.snapshot, attempt.id) {
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
		evidence := permissionguide.ReadRegistrationEvidence(ctx, bundle, service, attempt.started)
		mygo.RunOnMain(func() {
			attempt.checking = false
			if !evidence.Stale || s.closing || s.guideWindow != window || s.repairAttempt != attempt || s.currentID != attempt.id ||
				window.IsDestroyed() || !window.IsVisible() || !permissionGuideMayRepair(s.snapshot, attempt.id) {
				return
			}
			attempt.stale = true
			// Preflight also logs mismatches. Only a write to this service
			// during the drag proves which list accepted the app.
			if !evidence.Changed {
				window.Invalidate()
				return
			}
			fresh := s.readPermissionGuideSnapshot()
			if fresh.appBundlePath != bundle || !attempt.takeReset(fresh) {
				return
			}
			attempt.checking = true
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
				defer cancel()
				err := permissionguide.ResetOwnRegistration(ctx, service)
				mygo.RunOnMain(func() {
					attempt.checking = false
					if s.closing || s.guideWindow != window || s.repairAttempt != attempt || window.IsDestroyed() || !window.IsVisible() {
						return
					}
					if err != nil {
						log.Printf("permission registration reset %s: %v", service, err)
						window.Invalidate()
						return
					}
					attempt.resetDone = true
					log.Printf("permission registration reset %s: awaiting macOS authorization", service)
					s.refreshPermissionGuide()
					if s.repairAttempt != attempt || s.currentID != attempt.id || !permissionGuideRepairPending(s.snapshot, attempt.id) {
						return
					}
					switch attempt.id {
					case permissionGuideInput:
						_ = xiaomi.RequestListenAccess()
					case permissionGuidePostEvent:
						_ = textoutput.RequestAccess()
					}
				})
			}()
		})
	}()
}
