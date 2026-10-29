package main

import (
	"github.com/egoist/mygo/ui"
	"testing"
)

func TestVoiceOverlayRootHasNoOpaqueBackdrop(t *testing.T) {
	for _, dark := range []bool{false, true} {
		ordinary := ui.NewTester(func(c *ui.Context) {}, voiceOverlaySlot, voiceOverlaySlot)
		ordinary.SetDark(dark)
		if ordinary.Image().RGBAAt(80, 40).A == 0 {
			t.Fatal("control must paint the ordinary theme background")
		}
		overlay := &mygoVoiceOverlay{}
		view := ui.NewTester(overlay.draw, voiceOverlaySlot, voiceOverlaySlot)
		view.SetDark(dark)
		pixels := view.Image()
		for y := 0; y < voiceOverlaySlot; y++ {
			for x := 0; x < voiceOverlaySlot; x++ {
				if pixels.RGBAAt(x, y).A != 0 {
					t.Fatalf("dark=%v backdrop (%d,%d) is opaque", dark, x, y)
				}
			}
		}
		if len(view.Texts()) != 0 {
			t.Fatal("overlay must not add an explanatory caption")
		}
	}
}
