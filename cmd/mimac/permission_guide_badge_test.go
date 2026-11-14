package main

import (
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestPermissionGuideGrantedFeedbackHasSemanticCapsule(t *testing.T) {
	green := ui.RGB(0, 110, 20)
	v := ui.NewTester(func(c *ui.Context) {
		c.Root().Background(ui.Transparent)
		drawSuccessBadge(c, nil, green, green.Alpha(.08), "已授权")
	}, 100, 32)
	v.Frame()
	pixel := v.Image().RGBAAt(2, 12)
	if pixel.A < 18 || pixel.A > 22 || pixel.G <= pixel.R || pixel.G <= pixel.B {
		t.Fatalf("missing green capsule: %v", pixel)
	}
	if v.Image().RGBAAt(0, 0).A != 0 {
		t.Fatal("feedback lost its rounded transparent corner")
	}
	if v.FramePending() {
		t.Fatal("steady granted state schedules unnecessary animation frames")
	}
}

func TestPermissionGrantedTextKeepsReadableContrastInBothAppearances(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a := appAppearance(dark, 0)
		green := ui.RGB(52, 199, 89)
		background := green.Alpha(.08).Over(a.theme.Background)
		foreground := permissionGuideGrantedTextColor(green, background, a.theme.Text)
		if contrast := permissionGuideColorContrast(foreground, background); contrast < 4.5 {
			t.Fatalf("dark=%v, contrast=%v", dark, contrast)
		}
	}
	if got := permissionGuideColorContrast(ui.RGB(0, 0, 0), ui.RGB(255, 255, 255)); got < 20.99 || got > 21.01 {
		t.Fatalf("contrast scale=%v", got)
	}
}
