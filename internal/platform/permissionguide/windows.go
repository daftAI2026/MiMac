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
