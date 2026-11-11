package main

import (
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestPermissionGuideGrantedFeedbackHasSemanticCapsule(t *testing.T) {
	green := ui.RGB(0, 110, 20)
	v := ui.NewTester(func(c *ui.Context) {
		c.Root().Background(ui.Transparent)
		drawPermissionGuideGrantedBadge(c, nil, green)
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
