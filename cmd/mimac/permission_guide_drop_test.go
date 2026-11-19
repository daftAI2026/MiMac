package main

import (
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/platform/permissionguide"
)

func TestPermissionGuideDropReleaseRequiresTrackedSettingsContent(t *testing.T) {
	drop := permissionGuideDrop{
		active: true, targetID: 12,
		targetContent: permissionguide.Rect{X: 400, Y: 200, Width: 200, Height: 100},
	}
	if !drop.acceptsRelease(image.Pt(450, 250)) || drop.acceptsRelease(image.Pt(600, 250)) {
		t.Fatal("release must be inside the tracked content")
	}
	drop.active = false
	if drop.acceptsRelease(image.Pt(450, 250)) {
		t.Fatal("release must not reuse a completed drag target")
	}
}

func TestPermissionGuideDropMaskLeavesOnlyContentTransparent(t *testing.T) {
	for _, hole := range []ui.Rect{{X: 80, Y: 60, W: 140, H: 120}, {X: -60, Y: 60, W: 200, H: 120}, {X: 400, Y: 60, W: 140, H: 120}} {
		v := ui.NewTester(func(c *ui.Context) { drawPermissionGuideDrop(c, hole, false) }, 320, 240)
		v.Frame()
		img := v.Image()
		if alpha := img.RGBAAt(10, 10).A; alpha < 126 || alpha > 129 {
			t.Fatalf("outside alpha=%d", alpha)
		}
		if hole.X < 320 {
			x := int(max(hole.X+20, 20))
			y := int(hole.Y + 30)
			if alpha := img.RGBAAt(x, y).A; alpha != 0 {
				t.Fatalf("hole alpha=%d at %d,%d", alpha, x, y)
			}
		} else if alpha := img.RGBAAt(160, 120).A; alpha < 126 || alpha > 129 {
			t.Fatalf("other display should be fully dimmed: %d", alpha)
		}
		if hole.X > 0 && hole.X < 320 {
			if alpha := img.RGBAAt(int(hole.X), int(hole.Y)).A; alpha < 126 {
				t.Fatalf("rounded corner should be dimmed: %d", alpha)
			}
		}
		if v.FramePending() {
			t.Fatal("static mask schedules frames")
		}
	}
}

func TestPermissionGuideDropRimBrightensOutsideTheCutout(t *testing.T) {
	hole := ui.Rect{X: 80, Y: 60, W: 140, H: 120}
	capture := func(inside bool) *image.RGBA {
		v := ui.NewTester(func(c *ui.Context) { drawPermissionGuideDrop(c, hole, inside) }, 320, 240)
		v.Frame()
		return v.Image()
	}
	idle, active := capture(false), capture(true)
	rim := image.Pt(78, 120)
	if a, b := idle.RGBAAt(rim.X, rim.Y), active.RGBAAt(rim.X, rim.Y); a.R == 0 || b.R <= a.R {
		t.Fatalf("rim did not brighten: idle=%v active=%v", a, b)
	}
	if a := active.RGBAAt(81, 120).A; a != 0 {
		t.Fatalf("rim overlaps Settings content: alpha=%d", a)
	}
	if p := active.RGBAAt(76, 120); p != idle.RGBAAt(76, 120) || p.R != 0 {
		t.Fatalf("pointer changed the dimmed backdrop: %v", p)
	}
}

func TestPermissionGuideDropUsesSharedSettingsContent(t *testing.T) {
	r := permissionguide.Rect{X: 203, Y: 140, Width: 740, Height: 625}
	got := permissionGuideSettingsContent(r)
	want := permissionguide.Rect{X: 447, Y: 204, Width: 484, Height: 549}
	if got != want {
		t.Fatalf("content=%v, want %v", got, want)
	}
	if p := permissionGuideFlightDestination(r); float64(p.X) != got.X+got.Width/2 || float64(p.Y) != got.Y+130 {
		t.Fatal("illustration and actual drop target disagree")
	}
	tiny := permissionguide.Rect{X: -10, Y: 50, Width: 100, Height: 70}
	if permissionGuideSettingsContent(tiny) != tiny {
		t.Fatal("invalid crop must preserve original frame")
	}
}

func TestPermissionGuideDropStopReleasesTrackingButKeepsPreparedOwner(t *testing.T) {
	timer := time.AfterFunc(time.Hour, func() {})
	d := &permissionGuideDrop{active: true, run: 7, targetID: 42, timer: timer}
	s := &shell{permissionGuideState: permissionGuideState{dropGuide: d}}
	s.stopPermissionGuideDrop()
	if s.dropGuide != d || d.active || d.timer != nil || d.targetID != 0 || d.run != 8 {
		t.Fatalf("stopped drag owner=%+v", d)
	}
	if timer.Stop() {
		t.Fatal("tracking timer was not stopped")
	}
	s.stopPermissionGuideDrop()
	if d.run != 8 {
		t.Fatal("duplicate Done changed an inactive drag generation")
	}
	s.closePermissionGuideDrop()
	if s.dropGuide != nil || d.run != 9 {
		t.Fatal("closing the panel retained the drag owner")
	}
}
