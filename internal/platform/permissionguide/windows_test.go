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

func TestSettingsAttachedRestartDialogDefersPermissionRecovery(t *testing.T) {
	main := windowRecord{id: 39944, owner: 42, layer: 0, alpha: 1, frame: Rect{X: 781, Y: 145, Width: 740, Height: 625}}
	dialog := windowRecord{id: 39967, owner: 42, layer: 0, alpha: 1, frame: Rect{X: 1021, Y: 347, Width: 260, Height: 220}}
	if !settingsHasAttachedDialog([]windowRecord{dialog, main}, main.owner) {
		t.Fatal("missed the restart confirmation above the System Settings window")
	}
	anotherApp := dialog
	anotherApp.owner = 23
	outside := dialog
	outside.frame.X = 0
	for _, records := range [][]windowRecord{{main}, {main, dialog}, {anotherApp, main}, {outside, main}} {
		if settingsHasAttachedDialog(records, main.owner) {
			t.Fatalf("blocked independent permission recovery for %+v", records)
		}
	}
}

func TestPermissionDragMatchesPageEvenIfUserNavigated(t *testing.T) {
	if !settingsDropMatchesPage("Input Monitoring", true, false) ||
		settingsDropMatchesPage("Input Monitoring", false, true) ||
		settingsDropMatchesPage("Bluetooth", true, true) ||
		settingsDropMatchesPage("", true, false) ||
		!settingsDropMatchesPage("Device Control and Data Access", false, false) ||
		!settingsDropMatchesPage("设备控制和数据访问", false, false) ||
		!settingsDropMatchesPage("", true, true) {
		t.Fatal("permission drag did not respect the selected Settings page")
	}
}

func TestJoinOnlyWhenSettingsIsVisibleInWindowOrder(t *testing.T) {
	settings := windowRecord{id: 10, owner: 42, alpha: 1, frame: Rect{Width: 740, Height: 625}}
	setup := windowRecord{id: 11, owner: 43, alpha: 1, frame: settings.frame}
	card := windowRecord{id: 12, owner: 43, layer: 3, alpha: 1, frame: settings.frame}
	target := WindowSnapshot{ID: settings.id, Frame: settings.frame}
	if visibleSettingsTarget([]windowRecord{card, setup, settings}, target) {
		t.Fatal("setup covers Settings; the companion must remain in place")
	}
	if !visibleSettingsTarget([]windowRecord{card, settings, setup}, target) {
		t.Fatal("raising the existing Settings window should enable attachment")
	}
	if visibleSettingsTarget([]windowRecord{setup}, target) {
		t.Fatal("a missing window cannot be a visible join target")
	}
}
