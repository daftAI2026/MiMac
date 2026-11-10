package main

import (
	"encoding/json"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestPermissionGuideLayoutKeepsEveryRequiredRowVisible(t *testing.T) {
	for count := 1; count <= 3; count++ {
		for _, dark := range []bool{false, true} {
			for textSize := 0; textSize < 3; textSize++ {
				// The headless renderer has no AppKit event loop. Symbol pixels are
				// verified in the packaged window; reserve their normal slots here.
				s := &shell{settingsState: settingsState{navigationSymbols: make(map[symbolKey]*ui.Bitmap)}}
				s.navigationSymbols[symbolKey{name: "xmark", dark: true, size: 11}] = nil
				for _, palette := range []bool{false, true} {
					s.navigationSymbols[symbolKey{name: "arrow.up.right.circle", dark: palette, size: 16}] = nil
					for _, id := range []permissionGuideID{permissionGuideInput, permissionGuidePostEvent, permissionGuideBluetooth} {
						s.navigationSymbols[symbolKey{name: permissionGuideRowFor(id).icon, dark: palette, size: 18}] = nil
					}
				}
				s.preferencesState.value.TextSize = textSize
				for _, id := range []permissionGuideID{permissionGuideInput, permissionGuidePostEvent, permissionGuideBluetooth}[:count] {
					row := permissionGuideRowFor(id)
					row.access = permissionGuideGranted
					s.snapshot.rows = append(s.snapshot.rows, row)
				}
				height := permissionGuideWindowHeight(s.snapshot)
				view := ui.NewTester(s.permissionGuideView, permissionGuideWidth, height)
				view.SetDark(dark)
				view.Advance(2 * time.Millisecond)
				icon := s.iconBounds
				title, ok := view.Find("管理权限")
				if !ok || math.Abs(float64(title.Y-144)) > 1 || title.H != 30 {
					t.Fatalf("title geometry: %v %t", title, ok)
				}
				if icon.X != 202 || icon.Y != 36 || icon.W != 96 || icon.H != 96 {
					t.Fatalf("icon slot: %v", icon)
				}
				for _, row := range s.snapshot.rows {
					label, found := view.Find(row.title)
					description, hasDescription := view.Find(row.description)
					if !found || !hasDescription || label.X < 77 || label.X > 80 || description.Y+description.H > float32(height)-18 {
						t.Fatalf("row %s clipped: %v / %v at %d", row.title, label, description, height)
					}
				}
				// An explicit evidence destination does not alter the view or OS permissions.
				if out := os.Getenv("MIMAC_PERMISSION_UI_EVIDENCE"); out != "" && count == 3 && textSize == 1 {
					name := "light"
					if dark {
						name = "dark"
					}
					if err := os.MkdirAll(out, 0700); err != nil {
						t.Fatal(err)
					}
					f, err := os.Create(filepath.Join(out, "permission-"+name+".png"))
					if err != nil {
						t.Fatal(err)
					}
					if err := png.Encode(f, view.Image()); err != nil {
						t.Fatal(err)
					}
					if err := f.Close(); err != nil {
						t.Fatal(err)
					}
					data, _ := json.MarshalIndent(map[string]any{"icon": icon, "title": title, "height": height}, "", "  ")
					if err := os.WriteFile(filepath.Join(out, "permission-"+name+".json"), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
}

func TestPermissionGuideWrappedDescriptionsKeepControlsOnTheTopIconTrack(t *testing.T) {
	for _, description := range []string{"短说明。", "较长的说明会在权限名称下方自然换行，图标和右侧状态保持顶部对齐。", "这段说明用于覆盖至少三行文字。权限名称与说明保持左对齐，左侧图标和右侧授权状态保持顶部对齐，文字自然向下延展，不能挤压底部留白。"} {
		s := &shell{settingsState: settingsState{navigationSymbols: make(map[symbolKey]*ui.Bitmap)}}
		s.navigationSymbols[symbolKey{name: "xmark", dark: true, size: 11}] = nil
		s.navigationSymbols[symbolKey{name: "arrow.up.right.circle", size: 16}] = nil
		s.navigationSymbols[symbolKey{name: "keyboard", size: 18}] = nil
		row := permissionGuideRowFor(permissionGuideInput)
		row.description = description
		row.access = permissionGuideGranted
		s.snapshot.rows = []permissionGuideRow{row}
		view := ui.NewTester(s.permissionGuideView, 500, 600)
		view.Advance(2 * time.Millisecond)
		title, ok := view.Find(row.title)
		if !ok {
			t.Fatal("title absent")
		}
		text, ok := view.Find(description)
		if !ok {
			t.Fatal("description absent")
		}
		status, ok := view.Find("已授权")
		if !ok {
			t.Fatal("status absent")
		}
		expectedControlCenter := title.Y - (18-title.H)/2 + permissionGuideIconBox/2
		if math.Abs(float64(expectedControlCenter-status.Y-status.H/2)) > 1.5 {
			t.Fatalf("misaligned description height %.1f: title=%v text=%v status=%v", text.H, title, text, status)
		}
	}
}
