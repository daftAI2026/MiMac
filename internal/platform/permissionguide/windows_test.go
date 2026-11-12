package permissionguide

import "testing"

func TestSettingsWindowSelectionExcludesSheetsAndPreservesIdentity(t *testing.T) {
	main := windowRecord{id: 10, owner: 42, alpha: 1, frame: Rect{X: 200, Y: 140, Width: 740, Height: 625}}
	sheet := windowRecord{id: 11, owner: 42, alpha: 1, frame: Rect{X: 440, Y: 280, Width: 260, Height: 337}}
	other := windowRecord{id: 12, owner: 99, alpha: 1, frame: Rect{Width: 1920, Height: 1080}}
	next := windowRecord{id: 13, owner: 42, alpha: 1, frame: Rect{Width: 900, Height: 700}}
	if got, ok := selectSettingsWindow([]windowRecord{sheet, other, main}, 42, 0); !ok || got.ID != 10 {
		t.Fatalf("main with sheet=%+v/%v", got, ok)
	}
	if got, ok := selectSettingsWindow([]windowRecord{main, next}, 42, 0); !ok || got.ID != 13 {
		t.Fatalf("largest=%+v/%v", got, ok)
	}
	if got, ok := selectSettingsWindow([]windowRecord{main, next}, 42, 10); !ok || got.ID != 10 {
		t.Fatalf("pinned=%+v/%v", got, ok)
	}
	if _, ok := selectSettingsWindow([]windowRecord{next}, 42, 10); ok {
		t.Fatal("lost target switched to a new window")
	}
	for _, alpha := range []float64{0, .05} {
		w := main
		w.alpha = alpha
		if _, ok := selectSettingsWindow([]windowRecord{w}, 42, 0); ok {
			t.Fatal("transparent window selected")
		}
	}
	w := main
	w.layer = 3
	if _, ok := selectSettingsWindow([]windowRecord{w}, 42, 0); ok {
		t.Fatal("floating window selected")
	}
}

func TestSettingsOcclusionCountsUnionAndIgnoresGuidePanels(t *testing.T) {
	target := Rect{Width: 120, Height: 120}
	half := windowRecord{alpha: 1, frame: Rect{Width: 60, Height: 120}}
	if got := settingsWindowOcclusion(target, []windowRecord{half, half}); got != .5 {
		t.Fatalf("overlap double counted: %v", got)
	}
	half.layer = 4
	if got := settingsWindowOcclusion(target, []windowRecord{half}); got != 0 {
		t.Fatalf("guide covered target: %v", got)
	}
	half.layer = 0
	half.alpha = .05
	if got := settingsWindowOcclusion(target, []windowRecord{half}); got != 0 {
		t.Fatalf("transparent cover: %v", got)
	}
	half.alpha = 1
	half.frame.Width = 80
	if got := settingsWindowOcclusion(target, []windowRecord{half}); got < .6 {
		t.Fatalf("covered target retained: %v", got)
	}
}
