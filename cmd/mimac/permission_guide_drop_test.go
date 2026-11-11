package main

import (
	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/platform/permissionguide"
	"testing"
)

func TestPermissionGuideDropMaskLeavesOnlyContentTransparent(t *testing.T) {
	for _, hole := range []ui.Rect{{X: 80, Y: 60, W: 140, H: 120}, {X: -60, Y: 60, W: 200, H: 120}, {X: 400, Y: 60, W: 140, H: 120}} {
		v := ui.NewTester(func(c *ui.Context) { drawPermissionGuideDrop(c, hole) }, 320, 240)
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
