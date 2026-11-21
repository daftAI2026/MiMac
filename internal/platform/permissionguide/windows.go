package permissionguide

// Window facts are copied from one CGWindowList snapshot before selection.
type windowRecord struct {
	id           uint32
	owner, layer int32
	alpha        float64
	frame        Rect
}

func selectSettingsWindow(records []windowRecord, owner int32, id uint32) (WindowSnapshot, bool) {
	var selected WindowSnapshot
	area := float64(0)
	for _, w := range records {
		if w.owner != owner || w.layer != 0 || w.alpha <= .05 || w.frame.Width < 300 || w.frame.Height < 200 || w.id == 0 || (id != 0 && w.id != id) {
			continue
		}
		if candidateArea := w.frame.Width * w.frame.Height; candidateArea > area {
			selected = WindowSnapshot{ID: w.id, Frame: w.frame}
			area = candidateArea
		}
	}
	return selected, area > 0
}

func visibleSettingsTarget(records []windowRecord, target WindowSnapshot) bool {
	for i, window := range records {
		if window.id == target.ID {
			return settingsWindowOcclusion(target.Frame, records[:i]) < .6
		}
	}
	return false
}

// Count the union of ordinary windows over a fixed grid, without double
// counting overlapping occluders or including nonactivating guide panels.
func settingsWindowOcclusion(target Rect, above []windowRecord) float64 {
	if target.Width <= 0 || target.Height <= 0 {
		return 1
	}
	covered := 0
	for y := 0; y < 12; y++ {
		for x := 0; x < 12; x++ {
			px := target.X + (float64(x)+.5)*target.Width/12
			py := target.Y + (float64(y)+.5)*target.Height/12
			for _, w := range above {
				if w.layer == 0 && w.alpha > .05 && px >= w.frame.X && px < w.frame.X+w.frame.Width && py >= w.frame.Y && py < w.frame.Y+w.frame.Height {
					covered++
					break
				}
			}
		}
	}
	return float64(covered) / 144
}

// Settings presents its permission confirmation as a second window above and
// contained in the main window. Keep the native transaction intact while
// that window is visible, even when Accessibility is not granted yet.
func settingsHasAttachedDialog(records []windowRecord, owner int32) bool {
	main, ok := selectSettingsWindow(records, owner, 0)
	if !ok {
		return false
	}
	for _, record := range records {
		if record.id == main.ID {
			break
		}
		if record.owner != owner || record.layer != 0 || record.alpha <= .05 {
			continue
		}
		frame, outer := record.frame, main.Frame
		if frame.Width > 0 && frame.Height > 0 && frame.Width < outer.Width && frame.Height < outer.Height &&
			frame.X >= outer.X && frame.Y >= outer.Y && frame.X+frame.Width <= outer.X+outer.Width &&
			frame.Y+frame.Height <= outer.Y+outer.Height {
			return true
		}
	}
	return false
}

// CoreGraphics returns windows front to back. The pointer-transparent drag
// image and spotlight panels use floating layers above ordinary windows.
func settingsReceivesPoint(records []windowRecord, id uint32, owner int32, x, y float64) bool {
	for _, w := range records {
		r := w.frame
		if w.layer == 0 && w.alpha > .05 && x >= r.X && x < r.X+r.Width && y >= r.Y && y < r.Y+r.Height {
			return w.id == id && w.owner == owner
		}
	}
	return false
}

func settingsDropMatchesPage(title string, input, routed bool) bool {
	switch title {
	case "Input Monitoring", "输入监控", "輸入監控":
		return input
	case "Device Control and Data Access", "Accessibility", "设备控制和数据访问", "辅助功能", "裝置控制與資料存取", "輔助使用":
		return !input
	case "":
		// With no readable title, only an explicitly routed list is eligible.
		return routed
	default:
		return false
	}
}
