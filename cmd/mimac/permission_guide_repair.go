package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/platform/permissionguide"
)

const (
	permissionGuideDragRegistrationPreparationTTL         = 30 * time.Second
	permissionGuideRepairTop                      float32 = 12
	permissionGuideRepairInset                    float32 = 12
	permissionGuideRepairTextGap                  float32 = 4
)

// Keep only the existing inline failure hint. A failed pre-delivery attempt is
// shown to the user but is never retried from a later permission refresh.
type permissionGuideRepairAttempt struct {
	id     permissionGuideID
	failed bool
}

type permissionGuideDragRegistrationPreparation struct {
	id         permissionGuideID
	bundle     string
	generation uint64
	preparedAt time.Time
	result     chan *permissionguide.DragRegistrationProof
	ready      bool
	consumed   bool
	cancel     context.CancelFunc
	proof      *permissionguide.DragRegistrationProof
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
	if attempt == nil || !attempt.failed || !permissionGuideMayRepair(snapshot, attempt.id) {
		return "", ""
	}
	return permissionGuideRowFor(attempt.id).title + "授权更新未完成", "请在系统设置查看此项权限"
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

func permissionGuideRegistrationService(id permissionGuideID) (permissionguide.RegistrationService, bool) {
	switch id {
	case permissionGuideInput:
		return permissionguide.ListenRegistration, true
	case permissionGuidePostEvent:
		return permissionguide.AccessibilityRegistration, true
	default:
		return "", false
	}
}

func (p *permissionGuideDragRegistrationPreparation) reusable(now time.Time, id permissionGuideID, generation uint64, bundle string) bool {
	if p == nil || p.consumed || p.id != id || p.generation != generation || p.bundle != bundle ||
		p.preparedAt.IsZero() || now.Before(p.preparedAt) || now.Sub(p.preparedAt) >= permissionGuideDragRegistrationPreparationTTL {
		return false
	}
	return p.proof == nil || p.proof.Valid(now)
}

func (p *permissionGuideDragRegistrationPreparation) take(now time.Time, id permissionGuideID, generation uint64, bundle string) (*permissionguide.DragRegistrationProof, bool) {
	if p == nil || p.consumed || !p.ready || p.id != id || p.generation != generation || p.bundle != bundle {
		return nil, false
	}
	p.consumed = true
	proof := p.proof
	p.proof = nil
	if now.Before(p.preparedAt) || now.Sub(p.preparedAt) >= permissionGuideDragRegistrationPreparationTTL {
		permissionguide.DiscardDragRegistrationProof(proof)
		return nil, true
	}
	return proof, true
}

// cancelDragRegistrationPreparation bounds worker/watcher lifetime when the
// guide closes, the selected permission changes, or another drag supersedes it.
func (s *shell) cancelDragRegistrationPreparation() {
	preparation := s.dragRegistrationPreparation
	s.dragRegistrationPreparation = nil
	if preparation == nil {
		return
	}
	if preparation.cancel != nil {
		preparation.cancel()
	}
	permissionguide.DiscardDragRegistrationProof(preparation.proof)
	preparation.proof = nil
}

// prepareDragRegistration performs the expensive identity and TCC-log checks
// before a drag starts. It is read-only and only supports the two list rows.
func (s *shell) prepareDragRegistration(id permissionGuideID) {
	service, supported := permissionGuideRegistrationService(id)
	if !supported || s.closing || s.guideWindow == nil || s.guideWindow.IsDestroyed() ||
		!s.guideWindow.IsVisible() || !permissionGuideMayRepair(s.snapshot, id) || s.snapshot.appBundlePath == "" {
		return
	}
	if current := s.dragRegistrationPreparation; current != nil && current.id == id && current.generation == s.generation &&
		current.reusable(time.Now(), id, s.generation, s.snapshot.appBundlePath) {
		return
	}
	s.cancelDragRegistrationPreparation()
	window := s.guideWindow
	bundle := s.snapshot.appBundlePath
	preparation := &permissionGuideDragRegistrationPreparation{
		id: id, bundle: bundle, generation: s.generation, preparedAt: time.Now(), result: make(chan *permissionguide.DragRegistrationProof, 1),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	preparation.cancel = cancel
	s.dragRegistrationPreparation = preparation
	go func() {
		proof, err := permissionguide.PrepareDragRegistration(ctx, bundle, service)
		cancel()
		if err != nil {
			log.Printf("permission registration preflight %s: %v", service, err)
		}
		// Publish to the gesture before queuing UI work. AppKit may be
		// preparing its file offer on the main thread right now.
		preparation.result <- proof
		mygo.RunOnMain(func() {
			preparation.receive()
			if s.closing || s.guideWindow != window || s.dragRegistrationPreparation != preparation ||
				s.generation != preparation.generation || window.IsDestroyed() || !window.IsVisible() ||
				s.snapshot.appBundlePath != bundle || !permissionGuideMayRepair(s.snapshot, id) {
				permissionguide.DiscardDragRegistrationProof(preparation.proof)
				preparation.proof = nil
			}
		})
	}()
}

// Only the main thread consumes the result. The worker has its own deadline
// and always publishes before posting a completion callback to that thread.
func (p *permissionGuideDragRegistrationPreparation) receive() {
	if p.ready || p.result == nil {
		return
	}
	p.proof = <-p.result
	p.ready = true
}

// Input Monitoring checks at file delivery. Device Control checks at the
// confirmed start of the drag before AppKit can prefetch the file.
func (s *shell) repairDragRegistrationBeforeDelivery(id permissionGuideID) bool {
	service, supported := permissionGuideRegistrationService(id)
	preparation := s.dragRegistrationPreparation
	if !supported || s.closing || preparation == nil || preparation.id != id ||
		preparation.generation != s.generation || preparation.bundle != s.snapshot.appBundlePath ||
		preparation.consumed || !permissionGuideMayRepair(s.snapshot, id) {
		return false
	}
	preparation.receive()
	proof, taken := preparation.take(time.Now(), id, s.generation, s.snapshot.appBundlePath)
	if !taken {
		return false
	}
	defer permissionguide.DiscardDragRegistrationProof(proof)

	access := permissionguide.RegistrationAccessUnknown
	switch id {
	case permissionGuideInput:
		switch xiaomi.ListenAccess() {
		case xiaomi.ListenAccessGranted:
			access = permissionguide.RegistrationAccessGranted
		case xiaomi.ListenAccessDenied:
			access = permissionguide.RegistrationAccessDenied
		}
	case permissionGuidePostEvent:
		if granted, err := permissionguide.ReadAccessibilityAccess(); err == nil {
			if granted {
				access = permissionguide.RegistrationAccessGranted
			} else {
				access = permissionguide.RegistrationAccessDenied
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	reset, err := permissionguide.RepairDragRegistrationBeforeDelivery(ctx, proof, access)
	if err != nil {
		log.Printf("permission registration pre-delivery repair %s: %v", service, err)
		if permissionGuideRepairPending(s.snapshot, id) {
			s.repairAttempt = &permissionGuideRepairAttempt{id: id, failed: true}
			if s.guideWindow != nil && !s.guideWindow.IsDestroyed() {
				s.guideWindow.Invalidate()
			}
		}
		return false
	}
	if reset {
		log.Printf("permission registration %s: stale identity cleared before delivery", service)
	}
	return reset
}

// runPermissionRegistrationProbe is a separate, fresh-process readback only.
// It must never request access, reset a service, or initialize the host UI.
func runPermissionRegistrationProbe(args []string) error {
	if len(args) != 1 {
		return errors.New("expected a single permission registration service")
	}
	service := permissionguide.RegistrationService(args[0])
	if !permissionguide.CanProbeOwnRegistration(service) {
		return errors.New("unsupported permission registration identity")
	}
	status := "unknown"
	switch service {
	case permissionguide.ListenRegistration:
		switch xiaomi.ListenAccess() {
		case xiaomi.ListenAccessGranted:
			status = "granted"
		case xiaomi.ListenAccessDenied:
			status = "denied"
		}
	case permissionguide.AccessibilityRegistration:
		if granted, err := permissionguide.ReadAccessibilityAccess(); err == nil {
			if granted {
				status = "granted"
			} else {
				status = "denied"
			}
		}
	}
	fmt.Println(status)
	return nil
}
